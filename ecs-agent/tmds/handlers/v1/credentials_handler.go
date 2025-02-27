// Copyright Amazon.com Inc. or its affiliates. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License"). You may
// not use this file except in compliance with the License. A copy of the
// License is located at
//
//	http://aws.amazon.com/apache2.0/
//
// or in the "license" file accompanying this file. This file is distributed
// on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either
// express or implied. See the License for the specific language governing
// permissions and limitations under the License.

package v1

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/aws/amazon-ecs-agent/ecs-agent/credentials"
	"github.com/aws/amazon-ecs-agent/ecs-agent/logger/audit"
	auditinterface "github.com/aws/amazon-ecs-agent/ecs-agent/logger/audit"
	"github.com/aws/amazon-ecs-agent/ecs-agent/logger/audit/request"
	handlersutils "github.com/aws/amazon-ecs-agent/ecs-agent/tmds/handlers/utils"
	"github.com/aws/amazon-ecs-agent/ecs-agent/utils"
	"github.com/cihub/seelog"
)

const (
	// Error Types

	// ErrNoIDInRequest is the error code indicating that no ID was specified
	ErrNoIDInRequest = "NoIdInRequest"

	// ErrInvalidIDInRequest is the error code indicating that the ID was invalid
	ErrInvalidIDInRequest = "InvalidIdInRequest"

	// ErrNoCredentialsAssociated is the error code indicating no credentials are
	// associated with the specified ID
	ErrNoCredentialsAssociated = "NoCredentialsAssociated"

	// ErrCredentialsUninitialized is the error code indicating that credentials were
	// not properly initialized.  This may happen immediately after the agent is
	// started, before it has completed state reconciliation.
	ErrCredentialsUninitialized = "CredentialsUninitialized"

	// ErrInternalServer is the error indicating something generic went wrong
	ErrInternalServer = "InternalServerError"

	// Credentials API version.
	apiVersion = 1

	// CredentialsPath specifies the relative URI path for serving task IAM credentials
	CredentialsPath = credentials.V1CredentialsPath
)

// CredentialsHandler creates response for the 'v1/credentials' API. It returns a JSON response
// containing credentials when found. The HTTP status code of 400 is returned otherwise.
func CredentialsHandler(
	credentialsManager credentials.Manager,
	auditLogger auditinterface.AuditLogger,
) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		credentialsID := getCredentialsID(r)
		errPrefix := fmt.Sprintf("CredentialsV%dRequest: ", apiVersion)
		CredentialsHandlerImpl(w, r, auditLogger, credentialsManager, credentialsID, errPrefix)
	}
}

// CredentialsHandlerImpl is the major logic in CredentialsHandler, abstract this out
// because v2.CredentialsHandler also uses the same logic.
func CredentialsHandlerImpl(
	w http.ResponseWriter,
	r *http.Request,
	auditLogger auditinterface.AuditLogger,
	credentialsManager credentials.Manager,
	credentialsID string,
	errPrefix string,
) {
	responseJSON, arn, roleType, errorMessage, err := processCredentialsRequest(
		credentialsManager, r, credentialsID, errPrefix)
	if err != nil {
		errResponseJSON, err := json.Marshal(errorMessage)
		if e := handlersutils.WriteResponseIfMarshalError(w, err); e != nil {
			return
		}
		writeCredentialsRequestResponse(w, r, errorMessage.HTTPErrorCode,
			audit.GetCredentialsEventTypeFromRoleType(roleType), arn, auditLogger, errResponseJSON)
		return
	}

	writeCredentialsRequestResponse(w, r, http.StatusOK,
		audit.GetCredentialsEventTypeFromRoleType(roleType), arn, auditLogger, responseJSON)
}

// processCredentialsRequest returns the response json containing credentials for the
// credentials id in the request
func processCredentialsRequest(
	credentialsManager credentials.Manager,
	r *http.Request,
	credentialsID string,
	errPrefix string,
) ([]byte, string, string, *handlersutils.ErrorMessage, error) {
	if credentialsID == "" {
		errText := errPrefix + "No Credential ID in the request"
		seelog.Errorf("Error processing credential request: %s", errText)
		msg := &handlersutils.ErrorMessage{
			Code:          ErrNoIDInRequest,
			Message:       errText,
			HTTPErrorCode: http.StatusBadRequest,
		}
		return nil, "", "", msg, errors.New(errText)
	}

	credentials, ok := credentialsManager.GetTaskCredentials(credentialsID)
	if !ok {
		errText := errPrefix + "Credentials not found"
		seelog.Errorf("Error processing credential request: %s", errText)
		msg := &handlersutils.ErrorMessage{
			Code:          ErrInvalidIDInRequest,
			Message:       errText,
			HTTPErrorCode: http.StatusBadRequest,
		}
		return nil, "", "", msg, errors.New(errText)
	}

	seelog.Infof("Processing credential request, credentialType=%s taskARN=%s",
		credentials.IAMRoleCredentials.RoleType, credentials.ARN)

	if utils.ZeroOrNil(credentials.ARN) && utils.ZeroOrNil(credentials.IAMRoleCredentials) {
		// This can happen when the agent is restarted and is reconciling its state.
		errText := errPrefix + "Credentials uninitialized for ID"
		seelog.Errorf("Error processing credential request credentialType=%s taskARN=%s: %s",
			credentials.IAMRoleCredentials.RoleType, credentials.ARN, errText)
		msg := &handlersutils.ErrorMessage{
			Code:          ErrCredentialsUninitialized,
			Message:       errText,
			HTTPErrorCode: http.StatusServiceUnavailable,
		}
		return nil, "", "", msg, errors.New(errText)
	}

	credentialsJSON, err := json.Marshal(credentials.IAMRoleCredentials)
	if err != nil {
		errText := errPrefix + "Error marshaling credentials"
		seelog.Errorf("Error processing credential request credentialType=%s taskARN=%s: %s",
			credentials.IAMRoleCredentials.RoleType, credentials.ARN, errText)
		msg := &handlersutils.ErrorMessage{
			Code:          ErrInternalServer,
			Message:       "Internal server error",
			HTTPErrorCode: http.StatusInternalServerError,
		}
		return nil, "", "", msg, errors.New(errText)
	}

	// Success
	return credentialsJSON, credentials.ARN, credentials.IAMRoleCredentials.RoleType, nil, nil
}

func writeCredentialsRequestResponse(
	w http.ResponseWriter,
	r *http.Request,
	httpStatusCode int,
	eventType string,
	arn string,
	auditLogger auditinterface.AuditLogger,
	message []byte,
) {
	auditLogger.Log(request.LogRequest{Request: r, ARN: arn}, httpStatusCode, eventType)
	handlersutils.WriteJSONToResponse(w, httpStatusCode, message, handlersutils.RequestTypeCreds)
}

func getCredentialsID(r *http.Request) string {
	credentialsID, ok := handlersutils.ValueFromRequest(r, credentials.CredentialsIDQueryParameterName)
	if ok {
		return credentialsID
	}
	return ""
}

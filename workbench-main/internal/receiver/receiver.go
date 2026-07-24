package receiver

import (
	"context"
	"fmt"

	"time"

	"github.com/ONDC-Official/automation-beckn-plugins/workbench-main/internal/apiservice"
	"github.com/ONDC-Official/automation-beckn-plugins/workbench-main/internal/ondc/cache"
	"github.com/ONDC-Official/automation-beckn-plugins/workbench-main/internal/ondc/cache/sessioncache"
	"github.com/ONDC-Official/automation-beckn-plugins/workbench-main/internal/ondc/cache/subscribercache"
	"github.com/ONDC-Official/automation-beckn-plugins/workbench-main/internal/ondc/cache/transactioncache"
	"github.com/ONDC-Official/automation-beckn-plugins/workbench-main/internal/ondc/payloadutils"
	"github.com/beckn-one/beckn-onix/pkg/log"
)

type WorkbenchRequestReceiver struct {
	transactionCache transactioncache.Service
	subscriberCache  subscribercache.Service
	sessionCache     sessioncache.Service
}

func NewWorkbenchRequestReceiver(txnCache transactioncache.Service, subCache subscribercache.Service, sessCache sessioncache.Service) *WorkbenchRequestReceiver {
	return &WorkbenchRequestReceiver{
		transactionCache: txnCache,
		subscriberCache:  subCache,
		sessionCache:     sessCache,
	}
}

func (r *WorkbenchRequestReceiver) ReceiveFromNP(ctx context.Context, requestData *apiservice.WorkbenchRequestData) error {
	exists, err := r.transactionCache.CheckIfTransactionExists(
		ctx, r.transactionCache.CreateTransactionKey(requestData.TransactionID, requestData.SubscriberURL),
	)
	if err != nil {
		return payloadutils.NewInternalServerNackError(err.Error(), requestData.BodyRaw["context"])
	}
	if exists {
		return r.handleTransactionWhichExits(ctx, requestData)
	}
	subscriberData, _ := r.subscriberCache.LoadSubscriberThatExists(
		ctx, requestData.SubscriberURL,
	)
	if subscriberData != nil {
		err := r.tryFulfillExpectation(ctx, subscriberData, requestData)
		if( err != nil ){
			return err
		}
	}
	if(requestData.SessionID != ""){
		sessionData, sessionErr := r.sessionCache.LoadSessionThatExists(
			ctx, requestData.SessionID,
		)
		if sessionErr != nil {
			log.Errorf(ctx,sessionErr,"failed to load session data for session id: %s", requestData.SessionID)
			return payloadutils.NewInternalServerNackError(sessionErr.Error(), requestData.BodyRaw["context"])
		}
		requestData.Difficulty = sessionData.SessionDifficulty
		requestData.UsecaseID = sessionData.UsecaseId
		r.assignTransactionToSession(ctx, requestData.SessionID, requestData.FlowID, requestData.TransactionID)
		return nil
	}
	return payloadutils.NewPreconditionFailedHTTPError(
		fmt.Sprintf("No active expectation found for transaction ID: %s and Subscriber URL: %s for as a %s", requestData.TransactionID, requestData.SubscriberURL,requestData.RequestOwner),
	) 
}

func (r *WorkbenchRequestReceiver) ReceiveFromMock(ctx context.Context, requestData *apiservice.WorkbenchRequestData) error {
	exits, err := r.transactionCache.CheckIfTransactionExists(
		ctx, r.transactionCache.CreateTransactionKey(requestData.TransactionID, requestData.SubscriberURL),
	)
	if err != nil {
		return payloadutils.NewInternalServerNackError(err.Error(), requestData.BodyRaw["context"])
	}
	if exits {
		return r.handleTransactionWhichExits(ctx, requestData)
	}
	flowID,SessionID := requestData.Request.URL.Query().Get("flow_id"),requestData.Request.URL.Query().Get("session_id")
	if(flowID == "" || SessionID == ""){
		return payloadutils.NewBadRequestHTTPError("session_id and flow_id are required query parameters for mock requests")
	}
	requestData.FlowID = flowID
	requestData.SessionID = SessionID
	sessionData, sessionErr := r.sessionCache.LoadSessionThatExists(
		ctx, requestData.SessionID,
	)
	if sessionErr != nil {
		log.Errorf(ctx,sessionErr,"failed to load session data for session id: %s", requestData.SessionID)
		return payloadutils.NewInternalServerNackError(sessionErr.Error(), requestData.BodyRaw["context"])
	}
	if(sessionData == nil){
		return payloadutils.NewBadRequestHTTPError(
			fmt.Sprintf("session data not found for session id: %s", requestData.SessionID),
		)
	}
	requestData.Difficulty = sessionData.SessionDifficulty
	requestData.UsecaseID = sessionData.UsecaseId
	r.assignTransactionToSession(ctx, requestData.SessionID, requestData.FlowID, requestData.TransactionID)
	return nil
}

func (r *WorkbenchRequestReceiver) tryFulfillExpectation(ctx context.Context, subscriberData *cache.SubscriberCache, requestData *apiservice.WorkbenchRequestData) error {
	newExpectations := []cache.Expectation{}
	fulfilled := false
	log.Infof(ctx,"Trying to fulfill api expectaion for action: %s and subData: %+#v", requestData.BodyEnvelope.Context.Action, subscriberData)
	for _, expectation := range subscriberData.ActiveSessions {
		expiredTime,err := time.Parse(time.RFC3339,expectation.ExpireAt)
		if err != nil {
			log.Errorf(ctx,err,"invalid expectation expire time format: %v", expectation.ExpireAt)
			return payloadutils.NewInternalServerNackError("invalid expectation expire time", requestData.BodyRaw["context"])
		}
		nowTime := time.Now()
		if nowTime.After(expiredTime) {
			log.Warnf(ctx, "expectation expired at %v, now %v, %+#v", expiredTime, nowTime, expectation)
			continue
		}
		if expectation.ExpectedAction == requestData.BodyEnvelope.Context.Action && !fulfilled {
			requestData.FlowID = expectation.FlowId
			requestData.SessionID = expectation.SessionId
			fulfilled = true
			log.Infof(ctx, "expectation fulfilled: %+#v", expectation)
		}else {
			newExpectations = append(newExpectations, expectation)
		}
	}
	subscriberData.ActiveSessions = newExpectations
	err := r.subscriberCache.UpdateSubscriber(ctx,requestData.SubscriberURL,subscriberData,0)
	if err != nil {
		log.Errorf(ctx,err,"failed to update subscriber cache after fulfilling expectation")
	}
	return nil
}

func (r *WorkbenchRequestReceiver) handleTransactionWhichExits(
	ctx context.Context,
	requestData *apiservice.WorkbenchRequestData,
) error {

	transactionData, err := r.transactionCache.LoadTransactionThatExists(
		ctx, r.transactionCache.CreateTransactionKey(requestData.TransactionID, requestData.SubscriberURL),
	)
	if err != nil {
		return payloadutils.NewInternalServerNackError(err.Error(), requestData.BodyRaw["context"])
	}
	difficulty := r.defaultDifficulty()
	if transactionData.SessionId != "" {
		sessionData, sessionErr := r.sessionCache.LoadSessionThatExists(
			ctx, transactionData.SessionId,
		)
		if sessionErr != nil {
			return payloadutils.NewInternalServerNackError(sessionErr.Error(), requestData.BodyRaw["context"])
		}
		difficulty = sessionData.SessionDifficulty
		requestData.UsecaseID = sessionData.UsecaseId
	}
	requestData.Difficulty = difficulty
	requestData.FlowID = transactionData.FlowId
	requestData.SessionID = transactionData.SessionId

	return nil
}

func (r *WorkbenchRequestReceiver) defaultDifficulty() cache.SessionDifficulty {
	return cache.SessionDifficulty{
		SensitiveTTL:         true,
		UseGateway:           true, // routes search to GATEWAY_URL when true
		StopAfterFirstNack:   false,
		ProtocolValidations:  true,
		TimeValidations:      true,
		HeaderValidaton:      true,
		UseGzip:              false,
		EncryptionValidation: false, // opt-in — must be explicitly enabled per session
		UseCare:              false, // opt-in — routes IGM endpoints (issue, on_issue, issue_status, on_issue_status) to CARE_URL when true
		UseTunnelForFIS:      false, // opt-in - routes everything to tunnel for FIS flows when true
	}
}

func (r *WorkbenchRequestReceiver) assignTransactionToSession(
	ctx context.Context,
	sessionID string,
	flowID string,
	transactionID string,
){
	r.sessionCache.UpdateSessionCache(ctx,sessionID,flowID,transactionID,0)
}
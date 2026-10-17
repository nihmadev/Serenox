package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	pluginv1 "github.com/nihmadev/Serenox/pkg/pluginproto/v1"
)

type ReadScope struct {
	PluginID  string
	AccountID int64
}

type ReadClient interface {
	Read(context.Context, ReadScope, pluginv1.ClientRequest) (pluginv1.ClientResult, error)
}

type ReadBackend interface {
	GetMe(context.Context) (*pluginv1.Peer, error)
	ResolvePeer(context.Context, int64, string) (*pluginv1.Peer, error)
	GetMessage(context.Context, int64, int64) (*pluginv1.Message, error)
	GetHistory(context.Context, int64, int64, int, string) ([]pluginv1.Message, bool, error)
	SearchMessages(context.Context, int64, string, int64, string, int64, int) ([]pluginv1.Message, bool, error)
}

type ReadFixtureBackend interface {
	ReadFixture(context.Context, pluginv1.ClientRequest) (pluginv1.ClientResult, bool, error)
}

type ReadBatchBackend interface {
	GetMessages(context.Context, int64, []int64) ([]pluginv1.Message, error)
}

type ReadService struct {
	backend ReadBackend
	cursor  *CursorCodec
	sem     chan struct{}
}

func NewReadService(backend ReadBackend, cursor *CursorCodec, maxConcurrent int) (*ReadService, error) {
	if backend == nil || cursor == nil {
		return nil, errors.New("read service requires backend and cursor codec")
	}
	if maxConcurrent <= 0 || maxConcurrent > 64 {
		return nil, errors.New("read concurrency is outside its bound")
	}
	return &ReadService{backend: backend, cursor: cursor, sem: make(chan struct{}, maxConcurrent)}, nil
}

func (s *ReadService) Read(parent context.Context, scope ReadScope, request pluginv1.ClientRequest) (pluginv1.ClientResult, error) {
	if parent == nil {
		parent = context.Background()
	}
	if scope.PluginID == "" || scope.AccountID == 0 {
		return pluginv1.ClientResult{}, readFailure(request.Operation, pluginv1.ErrorPermissionDenied, "read scope is missing", false)
	}
	if err := request.Validate(); err != nil {
		return pluginv1.ClientResult{}, readFailure(request.Operation, pluginv1.ErrorInvalidRequest, err.Error(), false)
	}
	if request.Direction == "" && (request.Operation == pluginv1.ClientGetHistory || request.Operation == pluginv1.ClientGetHistoryAround || request.Operation == pluginv1.ClientSearchMessages || request.Operation == pluginv1.ClientSearchMessagesBySender || request.Operation == pluginv1.ClientSearchMessagesByMedia) {
		request.Direction = "older"
	}
	ctx, cancel := boundedReadContext(parent)
	defer cancel()
	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	case <-ctx.Done():
		return pluginv1.ClientResult{}, readContextFailure(request.Operation, ctx.Err())
	}
	position, page, err := s.position(scope, &request)
	if err != nil {
		return pluginv1.ClientResult{}, readCursorFailure(request.Operation, err)
	}
	if page >= pluginv1.MaxClientPages {
		return pluginv1.ClientResult{}, readCursorFailure(request.Operation, ErrCursorInvalid)
	}
	if position != 0 {
		request.OffsetID = position
	}
	result, err := s.dispatch(ctx, request)
	if err != nil {
		return pluginv1.ClientResult{}, mapReadError(request.Operation, err)
	}
	if err := result.Validate(); err != nil {
		return pluginv1.ClientResult{}, readFailure(request.Operation, pluginv1.ErrorConversion, err.Error(), false)
	}
	if err := s.addPage(scope, request, &result, page); err != nil {
		return pluginv1.ClientResult{}, err
	}
	data, _ := json.Marshal(result)
	if len(data) > int(pluginv1.DefaultMaxFrameSize) {
		return pluginv1.ClientResult{}, readFailure(request.Operation, pluginv1.ErrorResponseTruncated, "read response exceeds frame budget", false)
	}
	return result, nil
}

func (s *ReadService) dispatch(ctx context.Context, request pluginv1.ClientRequest) (pluginv1.ClientResult, error) {
	switch request.Operation {
	case pluginv1.ClientGetMe:
		value, err := s.backend.GetMe(ctx)
		return pluginv1.ClientResult{Me: value}, err
	case pluginv1.ClientResolvePeer, pluginv1.ClientSearchPublicUsername:
		value, err := s.backend.ResolvePeer(ctx, request.PeerID, request.Username)
		return pluginv1.ClientResult{Peer: value}, err
	case pluginv1.ClientGetMessage:
		value, err := s.backend.GetMessage(ctx, request.PeerID, request.MessageID)
		if err == nil && value == nil {
			return pluginv1.ClientResult{}, readFailure(request.Operation, pluginv1.ErrorMessageNotFound, "message was not found or was deleted", false)
		}
		return pluginv1.ClientResult{Message: value}, err
	case pluginv1.ClientGetMessages:
		batch, ok := s.backend.(ReadBatchBackend)
		if !ok {
			return pluginv1.ClientResult{}, readFailure(request.Operation, pluginv1.ErrorUnsupported, "bounded message batch is unavailable", false)
		}
		messages, err := batch.GetMessages(ctx, request.PeerID, request.MessageIDs)
		return pluginv1.ClientResult{Messages: messages}, err
	case pluginv1.ClientGetHistory, pluginv1.ClientGetHistoryAround:
		limit := request.Limit
		if limit == 0 {
			limit = pluginv1.MaxClientPageItems
		}
		before := request.Direction
		if before == "" {
			before = "older"
		}
		messages, more, err := s.backend.GetHistory(ctx, request.PeerID, request.OffsetID, limit, before)
		return pluginv1.ClientResult{Messages: messages, Page: &pluginv1.PageInfo{HasMore: more, BackendCallsUsed: 1}}, err
	case pluginv1.ClientSearchMessages, pluginv1.ClientSearchMessagesBySender, pluginv1.ClientSearchMessagesByMedia:
		limit := request.Limit
		if limit == 0 {
			limit = pluginv1.MaxClientPageItems
		}
		messages, more, err := s.backend.SearchMessages(ctx, request.PeerID, request.Query, request.SenderID, request.MediaKind, request.OffsetID, limit)
		return pluginv1.ClientResult{Messages: messages, Page: &pluginv1.PageInfo{HasMore: more, BackendCallsUsed: 1}}, err
	default:
		if fixture, ok := s.backend.(ReadFixtureBackend); ok {
			if result, found, err := fixture.ReadFixture(ctx, request); found || err != nil {
				return result, err
			}
		}
		return pluginv1.ClientResult{}, readFailure(request.Operation, pluginv1.ErrorUnsupported, "operation is not verified by this adapter", false)
	}
}

func (s *ReadService) position(scope ReadScope, request *pluginv1.ClientRequest) (int64, int, error) {
	if request.Cursor == "" {
		return 0, 0, nil
	}
	if request.OffsetID != 0 {
		return 0, 0, ErrCursorScope
	}
	return s.cursor.DecodeState(CursorScope{PluginID: scope.PluginID, AccountID: scope.AccountID, Operation: string(request.Operation), PeerID: request.PeerID, Query: request.Query, Direction: request.Direction}, request.Cursor)
}

func (s *ReadService) addPage(scope ReadScope, request pluginv1.ClientRequest, result *pluginv1.ClientResult, page int) error {
	if result.Page == nil || !result.Page.HasMore {
		return nil
	}
	position := int64(0)
	if len(result.Messages) != 0 {
		position = result.Messages[len(result.Messages)-1].ID
	} else if len(result.Dialogs) != 0 {
		position = result.Dialogs[len(result.Dialogs)-1].Peer.ID
	} else if len(result.Participants) != 0 {
		position = result.Participants[len(result.Participants)-1].User.ID
	} else {
		return readFailure(request.Operation, pluginv1.ErrorConversion, "adapter reported has_more without a cursorable item", false)
	}
	cursor, err := s.cursor.EncodePage(CursorScope{PluginID: scope.PluginID, AccountID: scope.AccountID, Operation: string(request.Operation), PeerID: request.PeerID, Query: request.Query, Direction: request.Direction}, position, page+1)
	if err != nil {
		return readCursorFailure(request.Operation, err)
	}
	result.Page.Cursor = cursor
	return nil
}

func boundedReadContext(parent context.Context) (context.Context, context.CancelFunc) {
	if _, ok := parent.Deadline(); ok {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, 15*time.Second)
}

func readContextFailure(operation pluginv1.ClientReadOperation, err error) error {
	if errors.Is(err, context.Canceled) {
		return readFailure(operation, pluginv1.ErrorDisconnected, "read canceled", true)
	}
	return readFailure(operation, pluginv1.ErrorDeadlineExceeded, "read deadline exceeded", true)
}

func readCursorFailure(operation pluginv1.ClientReadOperation, err error) error {
	code := pluginv1.ErrorCursorInvalid
	if errors.Is(err, ErrCursorExpired) {
		code = pluginv1.ErrorCursorExpired
	}
	return readFailure(operation, code, err.Error(), false)
}

func readFailure(operation pluginv1.ClientReadOperation, code pluginv1.ErrorCode, message string, retryable bool) error {
	return &pluginv1.Error{Code: code, Operation: string(operation), Message: message, Retryable: retryable}
}

func mapReadError(operation pluginv1.ClientReadOperation, err error) error {
	var protocolErr *pluginv1.Error
	if errors.As(err, &protocolErr) {
		if protocolErr.Operation == "" {
			protocolErr.Operation = string(operation)
		}
		return protocolErr
	}
	if errors.Is(err, context.Canceled) {
		return readFailure(operation, pluginv1.ErrorDisconnected, "Telegram read canceled", true)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return readFailure(operation, pluginv1.ErrorDeadlineExceeded, "Telegram read deadline exceeded", true)
	}
	if errors.Is(err, ErrPeerNotResolved) {
		return readFailure(operation, pluginv1.ErrorPeerNotFound, err.Error(), false)
	}
	if errors.Is(err, ErrNotAuthorized) {
		return readFailure(operation, pluginv1.ErrorDisconnected, err.Error(), true)
	}
	message := err.Error()
	upper := strings.ToUpper(message)
	if strings.Contains(upper, "FLOOD_WAIT") || strings.Contains(upper, "FLOOD WAIT") {
		return &pluginv1.Error{Code: pluginv1.ErrorFloodWait, Operation: string(operation), Message: "Telegram flood wait", Retryable: true}
	}
	if strings.Contains(upper, "FORBIDDEN") || strings.Contains(upper, "PERMISSION") {
		return readFailure(operation, pluginv1.ErrorPermissionDenied, "Telegram denied the read", false)
	}
	if strings.Contains(upper, "NOT_FOUND") || strings.Contains(upper, "NOT FOUND") {
		return readFailure(operation, pluginv1.ErrorPeerNotFound, "Telegram entity was not found", false)
	}
	return readFailure(operation, pluginv1.ErrorInternal, "Telegram read failed", true)
}

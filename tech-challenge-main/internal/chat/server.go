package chat

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"strings"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/singleflight"

	"github.com/acai-travel/tech-challenge/internal/chat/model"
	"github.com/acai-travel/tech-challenge/internal/pb"
	"github.com/twitchtv/twirp"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

var _ pb.ChatService = (*Server)(nil)

type Assistant interface {
	Title(ctx context.Context, conv *model.Conversation) (string, error)
	Reply(ctx context.Context, conv *model.Conversation) (string, error)
}

type Server struct {
	repo   *model.Repository
	assist Assistant

	// Caching for titles
	titleLRU *lru.Cache[string, string]
	titleSF  singleflight.Group
}

// NewServer initializes the server with an in-memory LRU for titles.
// Size is tunable; 10k entries is plenty for most deployments.
func NewServer(repo *model.Repository, assist Assistant) *Server {
	cache, _ := lru.New[string, string](10_000)
	return &Server{
		repo:     repo,
		assist:   assist,
		titleLRU: cache,
	}
}

func (s *Server) StartConversation(ctx context.Context, req *pb.StartConversationRequest) (*pb.StartConversationResponse, error) {
	if strings.TrimSpace(req.GetMessage()) == "" {
		return nil, twirp.RequiredArgumentError("message")
	}

	now := time.Now()
	conversation := &model.Conversation{
		ID:        primitive.NewObjectID(),
		Title:     "Untitled conversation",
		CreatedAt: now,
		UpdatedAt: now,
		Messages: []*model.Message{{
			ID:        primitive.NewObjectID(),
			Role:      model.RoleUser,
			Content:   req.GetMessage(),
			CreatedAt: now,
			UpdatedAt: now,
		}},
	}

	// Persist early so we never lose the user's first message.
	if err := s.repo.CreateConversation(ctx, conversation); err != nil {
		return nil, err
	}

	// Request-scoped timeout & cancellation for both calls.
	ctxReq, cancelReq := context.WithTimeout(ctx, 30*time.Second)
	defer cancelReq()

	// Adaptive title budget: up to 15s but never beyond req deadline - 500ms.
	titleBudget := 15 * time.Second
	if dl, ok := ctxReq.Deadline(); ok {
		rem := time.Until(dl) - 500*time.Millisecond
		if rem < titleBudget {
			if rem <= 0 {
				rem = 500 * time.Millisecond
			}
			titleBudget = rem
		}
	}

	var (
		title string
		reply string
	)

	g, gctx := errgroup.WithContext(ctxReq)

	// Title (cached + singleflight), with its own sub-timeout
	g.Go(func() error {
		tctx, cancel := context.WithTimeout(gctx, titleBudget)
		defer cancel()

		t, err := s.generateTitle(tctx, conversation)
		if err != nil || strings.TrimSpace(t) == "" {
			slog.WarnContext(gctx, "Title generation failed or empty; keeping default", "error", err)
			return nil // non-fatal
		}
		title = strings.TrimSpace(t)
		return nil
	})

	// Reply (required)
	g.Go(func() error {
		r, err := s.generateReply(gctx, conversation)
		if err != nil {
			return err
		}
		reply = r
		return nil
	})

	// If reply errors or context cancels, this returns early and cancels the sibling.
	if err := g.Wait(); err != nil {
		return nil, twirp.InternalErrorWith(err)
	}

	// Update conversation with results
	if title != "" {
		conversation.Title = title
	}
	now = time.Now()
	conversation.UpdatedAt = now
	conversation.Messages = append(conversation.Messages, &model.Message{
		ID:        primitive.NewObjectID(),
		Role:      model.RoleAssistant,
		Content:   reply,
		CreatedAt: now,
		UpdatedAt: now,
	})

	if err := s.repo.UpdateConversation(ctxReq, conversation); err != nil {
		// Non-fatal: we already have the reply to return
		slog.ErrorContext(ctxReq, "Failed to update conversation", "error", err)
	}

	return &pb.StartConversationResponse{
		ConversationId: conversation.ID.Hex(),
		Title:          conversation.Title,
		Reply:          reply,
	}, nil
}

// ---- Helpers ----

func (s *Server) generateTitle(ctx context.Context, conv *model.Conversation) (string, error) {
	// Cache key includes a normalized “first message”; if you change prompt or model,
	// bump the version string so old cache entries don’t conflict.
	key := s.makeTitleKey(conv, "o1", "v1")

	// LRU hit
	if v, ok := s.titleLRU.Get(key); ok {
		return v, nil
	}

	// Collapse duplicate inflight requests
	v, err, _ := s.titleSF.Do(key, func() (any, error) {
		t, err := s.assist.Title(ctx, conv)
		if err == nil {
			nt := normalizeTitle(t)
			if nt != "" {
				s.titleLRU.Add(key, nt)
				return nt, nil
			}
		}
		return t, err
	})
	if err != nil {
		return "", err
	}
	return v.(string), nil
}

func (s *Server) generateReply(ctx context.Context, conv *model.Conversation) (string, error) {
	// If you later add reply caching, be careful: replies are time- and context-sensitive.
	// For now, call through.
	return s.assist.Reply(ctx, conv)
}

// ---- Cache key helpers ----

func (s *Server) makeTitleKey(conv *model.Conversation, model string, promptVersion string) string {
	first := ""
	if len(conv.Messages) > 0 && conv.Messages[0] != nil {
		first = normalizeForKey(conv.Messages[0].Content)
	}
	// Include model + prompt version to avoid cross-version collisions.
	raw := strings.Join([]string{"title", model, promptVersion, first}, "|")
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func normalizeForKey(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	// collapse internal whitespace to single spaces
	fields := strings.Fields(s)
	return strings.Join(fields, " ")
}

func normalizeTitle(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) > 80 {
		s = s[:80]
	}
	return s
}

func (s *Server) ContinueConversation(ctx context.Context, req *pb.ContinueConversationRequest) (*pb.ContinueConversationResponse, error) {
	if req.GetConversationId() == "" {
		return nil, twirp.RequiredArgumentError("conversation_id")
	}
	if strings.TrimSpace(req.GetMessage()) == "" {
		return nil, twirp.RequiredArgumentError("message")
	}

	conversation, err := s.repo.DescribeConversation(ctx, req.GetConversationId())
	if err != nil {
		return nil, err
	}

	conversation.UpdatedAt = time.Now()
	conversation.Messages = append(conversation.Messages, &model.Message{
		ID:        primitive.NewObjectID(),
		Role:      model.RoleUser,
		Content:   req.GetMessage(),
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	})

	reply, err := s.assist.Reply(ctx, conversation)
	if err != nil {
		return nil, twirp.InternalErrorWith(err)
	}

	conversation.Messages = append(conversation.Messages, &model.Message{
		ID:        primitive.NewObjectID(),
		Role:      model.RoleAssistant,
		Content:   reply,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	})

	if err := s.repo.UpdateConversation(ctx, conversation); err != nil {
		return nil, twirp.InternalErrorWith(err)
	}

	return &pb.ContinueConversationResponse{Reply: reply}, nil
}

func (s *Server) ListConversations(ctx context.Context, req *pb.ListConversationsRequest) (*pb.ListConversationsResponse, error) {
	conversations, err := s.repo.ListConversations(ctx)
	if err != nil {
		return nil, twirp.InternalErrorWith(err)
	}

	resp := &pb.ListConversationsResponse{}
	for _, conv := range conversations {
		conv.Messages = nil // Clear messages to avoid sending large data
		resp.Conversations = append(resp.Conversations, conv.Proto())
	}
	return resp, nil
}

func (s *Server) DescribeConversation(ctx context.Context, req *pb.DescribeConversationRequest) (*pb.DescribeConversationResponse, error) {
	if req.GetConversationId() == "" {
		return nil, twirp.RequiredArgumentError("conversation_id")
	}

	conversation, err := s.repo.DescribeConversation(ctx, req.GetConversationId())
	if err != nil {
		return nil, err
	}
	if conversation == nil {
		return nil, twirp.NotFoundError("conversation not found")
	}
	return &pb.DescribeConversationResponse{Conversation: conversation.Proto()}, nil
}

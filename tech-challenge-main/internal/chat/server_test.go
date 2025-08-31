package chat

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/acai-travel/tech-challenge/internal/chat/model"
	. "github.com/acai-travel/tech-challenge/internal/chat/testing"
	"github.com/acai-travel/tech-challenge/internal/pb"
	"github.com/google/go-cmp/cmp"
	"github.com/twitchtv/twirp"
	"google.golang.org/protobuf/testing/protocmp"
)

// -----------------------------------------------------------------------------
// fakes
// -----------------------------------------------------------------------------

type fakeAssistant struct {
	mu         sync.Mutex
	titleCalls int
	replyCalls int

	titleFn func(ctx context.Context, conv *model.Conversation) (string, error)
	replyFn func(ctx context.Context, conv *model.Conversation) (string, error)
}

func (f *fakeAssistant) Title(ctx context.Context, conv *model.Conversation) (string, error) {
	f.mu.Lock()
	f.titleCalls++
	f.mu.Unlock()
	return f.titleFn(ctx, conv)
}
func (f *fakeAssistant) Reply(ctx context.Context, conv *model.Conversation) (string, error) {
	f.mu.Lock()
	f.replyCalls++
	f.mu.Unlock()
	return f.replyFn(ctx, conv)
}

// -----------------------------------------------------------------------------
// existing tests
// -----------------------------------------------------------------------------

func TestServer_DescribeConversation(t *testing.T) {
	ctx := context.Background()
	srv := NewServer(model.New(ConnectMongo()), nil)

	t.Run("describe existing conversation", WithFixture(func(t *testing.T, f *Fixture) {
		c := f.CreateConversation()

		out, err := srv.DescribeConversation(ctx, &pb.DescribeConversationRequest{ConversationId: c.ID.Hex()})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		got, want := out.GetConversation(), c.Proto()
		if !cmp.Equal(got, want, protocmp.Transform()) {
			t.Errorf("DescribeConversation() mismatch (-got +want):\n%s",
				cmp.Diff(got, want, protocmp.Transform()))
		}
	}))

	t.Run("describe non existing conversation should return 404", WithFixture(func(t *testing.T, f *Fixture) {
		_, err := srv.DescribeConversation(ctx, &pb.DescribeConversationRequest{
			ConversationId: "08a59244257c872c5943e2a2",
		})
		if err == nil {
			t.Fatal("expected error for non-existing conversation, got nil")
		}

		if te, ok := err.(twirp.Error); !ok || te.Code() != twirp.NotFound {
			t.Fatalf("expected twirp.NotFound error, got %v", err)
		}
	}))
}

// -----------------------------------------------------------------------------
// new tests for StartConversation (title + reply logic, perf, caching)
// -----------------------------------------------------------------------------

func TestStartConversation_Happy(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := model.New(ConnectMongo())
	fa := &fakeAssistant{
		titleFn: func(ctx context.Context, c *model.Conversation) (string, error) {
			return "Weather in Barcelona", nil
		},
		replyFn: func(ctx context.Context, c *model.Conversation) (string, error) {
			return "It’s sunny!", nil
		},
	}
	srv := NewServer(repo, fa)

	out, err := srv.StartConversation(ctx, &pb.StartConversationRequest{
		Message: "What is the weather like in Barcelona?",
	})
	if err != nil {
		t.Fatalf("StartConversation error: %v", err)
	}
	if got, want := out.GetTitle(), "Weather in Barcelona"; got != want {
		t.Fatalf("title: got %q want %q", got, want)
	}
	if got, want := out.GetReply(), "It’s sunny!"; got != want {
		t.Fatalf("reply: got %q want %q", got, want)
	}

	conv, err := repo.DescribeConversation(ctx, out.GetConversationId())
	if err != nil {
		t.Fatalf("DescribeConversation error: %v", err)
	}
	if got := len(conv.Messages); got != 2 {
		t.Fatalf("expected 2 messages (user+assistant), got %d", got)
	}
}

func TestStartConversation_TitleFailureIsNonFatal(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := model.New(ConnectMongo())
	fa := &fakeAssistant{
		titleFn: func(ctx context.Context, c *model.Conversation) (string, error) {
			return "", errors.New("boom")
		},
		replyFn: func(ctx context.Context, c *model.Conversation) (string, error) {
			return "ok", nil
		},
	}
	srv := NewServer(repo, fa)

	out, err := srv.StartConversation(ctx, &pb.StartConversationRequest{Message: "x"})
	if err != nil {
		t.Fatalf("StartConversation error: %v", err)
	}
	if out.GetTitle() == "" {
		t.Fatal("expected non-empty title (default or cached)")
	}
	if got := out.GetReply(); got != "ok" {
		t.Fatalf("reply: got %q want %q", got, "ok")
	}
}

func TestStartConversation_ParallelLatency(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := model.New(ConnectMongo())

	// simulate two ~150ms calls; total should be ~150–230ms, not ~300ms
	fa := &fakeAssistant{
		titleFn: func(ctx context.Context, c *model.Conversation) (string, error) {
			time.Sleep(150 * time.Millisecond)
			return "T", nil
		},
		replyFn: func(ctx context.Context, c *model.Conversation) (string, error) {
			time.Sleep(150 * time.Millisecond)
			return "R", nil
		},
	}
	srv := NewServer(repo, fa)

	start := time.Now()
	if _, err := srv.StartConversation(ctx, &pb.StartConversationRequest{Message: "parallel test"}); err != nil {
		t.Fatalf("StartConversation error: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed > 250*time.Millisecond {
		t.Fatalf("expected parallel speedup; elapsed=%v (too slow)", elapsed)
	}
}

func TestStartConversation_TitleCachingAndSingleflight(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := model.New(ConnectMongo())

	startGate := make(chan struct{})
	fa := &fakeAssistant{
		titleFn: func(ctx context.Context, c *model.Conversation) (string, error) {
			<-startGate
			time.Sleep(50 * time.Millisecond) // widen overlap window
			return "Cached Title", nil
		},
		replyFn: func(ctx context.Context, c *model.Conversation) (string, error) {
			return "R", nil
		},
	}
	srv := NewServer(repo, fa)

	var wg sync.WaitGroup
	wg.Add(2)
	errs := make([]error, 2)

	go func() {
		defer wg.Done()
		_, errs[0] = srv.StartConversation(ctx, &pb.StartConversationRequest{Message: "same input"})
	}()
	go func() {
		defer wg.Done()
		_, errs[1] = srv.StartConversation(ctx, &pb.StartConversationRequest{Message: "same input"})
	}()

	close(startGate)
	wg.Wait()
	if errs[0] != nil || errs[1] != nil {
		t.Fatalf("StartConversation errors: %v %v", errs[0], errs[1])
	}

	fa.mu.Lock()
	tc := fa.titleCalls
	fa.mu.Unlock()
	if tc != 1 {
		t.Fatalf("expected title to be computed once, got %d calls", tc)
	}
}

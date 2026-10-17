package scenario

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

// The paginated list clause (compat/model/README.md § Paginating a list): a
// walk to the last page, a match against the union of the pages, a floor on how
// many pages came back, and a cap so a service that never stops fails instead
// of hanging. The calls are the two the emitter writes for a paginated clause —
// page one with the page size set, and the same with the input token read from
// PageTokenKey — over a fake ListQueues that answers from a script and records
// every input it was sent.

// fakeQueues answers ListQueues from pages in order, the last one repeating.
type fakeQueues struct {
	pages []*sqs.ListQueuesOutput
	err   map[int]error // call number (0-based) → the error to raise
	seen  []*sqs.ListQueuesInput
}

func (f *fakeQueues) send(_ context.Context, in any) (any, error) {
	n := len(f.seen)
	f.seen = append(f.seen, in.(*sqs.ListQueuesInput))
	if err := f.err[n]; err != nil {
		return nil, err
	}
	return f.pages[min(n, len(f.pages)-1)], nil
}

func (f *fakeQueues) sent() []string {
	out := make([]string, 0, len(f.seen))
	for _, in := range f.seen {
		out = append(out, fmt.Sprintf("size=%d token=%q", aws.ToInt32(in.MaxResults), aws.ToString(in.NextToken)))
	}
	return out
}

func queuePage(token string, urls ...string) *sqs.ListQueuesOutput {
	out := &sqs.ListQueuesOutput{QueueUrls: urls}
	if token != "" {
		out.NextToken = aws.String(token)
	}
	return out
}

// pagedList is the clause the emitter writes: kind selects ListContains or
// AbsentFromList, and want is the url the where entry looks for.
func pagedList(f *fakeQueues, kind, want string, minPages int) Clause {
	first := Call{
		Op:     "ListQueues",
		Params: `{"MaxResults":1,"QueueNamePrefix":"oc-"}`,
		Build: func(b *Binder) any {
			in := &sqs.ListQueuesInput{}
			in.MaxResults = aws.Int32(1)
			in.QueueNamePrefix = aws.String("oc-")
			return in
		},
		Send: f.send,
	}
	next := Call{
		Op:     "ListQueues",
		Params: `{"MaxResults":1,"NextToken":{"$ref":"$pageToken"},"QueueNamePrefix":"oc-"}`,
		Build: func(b *Binder) any {
			in := &sqs.ListQueuesInput{}
			in.MaxResults = aws.Int32(1)
			in.NextToken = aws.String(Bind[string](b, "NextToken", Ref(PageTokenKey)))
			in.QueueNamePrefix = aws.String("oc-")
			return in
		},
		Send: f.send,
	}
	build := ListContains
	if kind == KindAbsent {
		build = AbsentFromList
	}
	return Paginated(build(&first, "$.QueueUrls", Where("$", want)),
		Page{Size: 1, MinPages: minPages, OutputToken: "NextToken", Next: next})
}

func runPaged(clause Clause) error {
	return testGroup.RunTest(context.Background(), testContext(), "ListQueues", Test{
		Call:   createQueue(&sqsCreated, nil),
		Assert: []Clause{clause},
	})
}

var sqsCreated = sqs.CreateQueueOutput{QueueUrl: aws.String("http://q/created")}

func TestPaginated_walksEveryPageFeedingTheTokenBack(t *testing.T) {
	f := &fakeQueues{pages: []*sqs.ListQueuesOutput{
		queuePage("t1", "http://a"), queuePage("t2", "http://b"), queuePage("", "http://q"),
	}}
	if err := runPaged(pagedList(f, KindListContains, "http://q", 0)); err != nil {
		t.Fatalf("a walk to the last page failed: %v", err)
	}
	want := `size=1 token="" | size=1 token="t1" | size=1 token="t2"`
	if got := strings.Join(f.sent(), " | "); got != want {
		t.Errorf("inputs sent = %s, want %s", got, want)
	}
}

func TestPaginated_keepsWalkingAfterTheItemIsFound(t *testing.T) {
	f := &fakeQueues{pages: []*sqs.ListQueuesOutput{queuePage("t1", "http://q"), queuePage("", "http://b")}}
	if err := runPaged(pagedList(f, KindListContains, "http://q", 0)); err != nil {
		t.Fatalf("%v", err)
	}
	if len(f.seen) != 2 {
		t.Errorf("%d pages fetched, want both: the walk always reaches the end", len(f.seen))
	}
}

func TestPaginated_aMissSaysHowFarTheWalkGotAndQuotesPageOnesParams(t *testing.T) {
	f := &fakeQueues{pages: []*sqs.ListQueuesOutput{
		queuePage("t1", "http://a"), queuePage("t2", "http://b"), queuePage("", "http://c"),
	}}
	err := runPaged(pagedList(f, KindListContains, "http://q", 0))
	if err == nil {
		t.Fatal("an item on no page passed")
	}
	msg := err.Error()
	for _, want := range []string{"walked 3 page(s), 3 item(s): ", `"http://a"`, `"http://c"`, "listContains at $.QueueUrls", `"MaxResults":1`} {
		if !strings.Contains(msg, want) {
			t.Errorf("failure %q lacks %q", msg, want)
		}
	}
	if strings.Contains(strings.SplitN(msg, "expected", 2)[0], "NextToken") {
		t.Errorf("failure %q quotes a later page's params", msg)
	}
}

func TestPaginated_listContainsStillNeedsANonEmptyList(t *testing.T) {
	for name, pages := range map[string][]*sqs.ListQueuesOutput{
		"empty":        {queuePage("")},
		"empty, paged": {queuePage("t"), queuePage("")},
	} {
		t.Run(name, func(t *testing.T) {
			err := runPaged(pagedList(&fakeQueues{pages: pages}, KindListContains, "http://q", 0))
			if err == nil || !strings.Contains(err.Error(), "walked ") {
				t.Fatalf("err = %v, want a failure naming the walk", err)
			}
		})
	}
}

func TestPaginated_minPagesFailsASinglePageThatHoldsEverything(t *testing.T) {
	err := runPaged(pagedList(&fakeQueues{pages: []*sqs.ListQueuesOutput{queuePage("", "http://q")}}, KindListContains, "http://q", 2))
	if err == nil {
		t.Fatal("a single page passed a clause that wants two")
	}
	for _, want := range []string{"at least 2 page(s) at a page size of 1", "walked 1 page(s), 1 item(s)"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("failure %q lacks %q", err, want)
		}
	}
	two := &fakeQueues{pages: []*sqs.ListQueuesOutput{queuePage("t", "http://a"), queuePage("", "http://q")}}
	if err := runPaged(pagedList(two, KindListContains, "http://q", 2)); err != nil {
		t.Errorf("two pages failed minPages 2: %v", err)
	}
}

func TestPaginated_aServiceThatNeverStopsFailsAtThePageCap(t *testing.T) {
	f := &fakeQueues{pages: []*sqs.ListQueuesOutput{queuePage("again", "http://a")}}
	err := runPaged(pagedList(f, KindListContains, "http://q", 0))
	if err == nil {
		t.Fatal("a service that always returns a token passed")
	}
	if len(f.seen) != MaxPages {
		t.Errorf("%d pages fetched, want exactly %d", len(f.seen), MaxPages)
	}
	for _, want := range []string{
		fmt.Sprintf("no NextToken on a page within %d pages", MaxPages),
		fmt.Sprintf("walked %d page(s), %d item(s), and the last still returned a NextToken", MaxPages, MaxPages),
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("failure %q lacks %q", err, want)
		}
	}
}

func TestPaginated_absentLooksAtEveryPage(t *testing.T) {
	clean := &fakeQueues{pages: []*sqs.ListQueuesOutput{queuePage("t", "http://a"), queuePage("", "http://b")}}
	if err := runPaged(pagedList(clean, KindAbsent, "http://q", 0)); err != nil {
		t.Errorf("absent failed on a list without the item: %v", err)
	}
	dirty := &fakeQueues{pages: []*sqs.ListQueuesOutput{queuePage("t", "http://a"), queuePage("", "http://q")}}
	err := runPaged(pagedList(dirty, KindAbsent, "http://q", 0))
	if err == nil || !strings.Contains(err.Error(), "walked 2 page(s), 2 item(s): ") {
		t.Errorf("err = %v, want absent to fail on the item on page two", err)
	}
}

func TestPaginated_aCallFailingOnALaterPageIsACallFailure(t *testing.T) {
	f := &fakeQueues{
		pages: []*sqs.ListQueuesOutput{queuePage("t1", "http://a")},
		err:   map[int]error{1: errors.New("boom")},
	}
	err := runPaged(pagedList(f, KindListContains, "http://q", 0))
	if err == nil {
		t.Fatal("a failing second page passed")
	}
	for _, want := range []string{"call", "boom", `"NextToken":"t1"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("failure %q lacks %q", err, want)
		}
	}
}

func TestPaginated_insideEventuallyEveryAttemptWalksAgain(t *testing.T) {
	f := &fakeQueues{pages: []*sqs.ListQueuesOutput{
		queuePage("t1", "http://a"), queuePage("", "http://b"), // attempt 1: no match
		queuePage("t1", "http://a"), queuePage("", "http://q"), // attempt 2: match
	}}
	clause := Eventually(3, 0, pagedList(f, KindListContains, "http://q", 0))
	if err := runPaged(clause); err != nil {
		t.Fatalf("eventually did not retry the walk: %v", err)
	}
	want := `size=1 token="" | size=1 token="t1" | size=1 token="" | size=1 token="t1"`
	if got := strings.Join(f.sent(), " | "); got != want {
		t.Errorf("inputs sent = %s, want %s: the second attempt starts again from page one", got, want)
	}
}

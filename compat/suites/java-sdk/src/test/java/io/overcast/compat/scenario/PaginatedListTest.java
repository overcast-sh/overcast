package io.overcast.compat.scenario;

import io.overcast.compat.harness.TestContext;
import org.junit.jupiter.api.Test;
import software.amazon.awssdk.services.sqs.model.CreateQueueRequest;
import software.amazon.awssdk.services.sqs.model.CreateQueueResponse;
import software.amazon.awssdk.services.sqs.model.ListQueuesRequest;
import software.amazon.awssdk.services.sqs.model.ListQueuesResponse;

import java.util.ArrayList;
import java.util.List;
import java.util.function.Function;

import static org.junit.jupiter.api.Assertions.assertDoesNotThrow;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * The paginated list clause ({@code compat/model/README.md} § Paginating a
 * list): a walk to the last page, a match against the union of the pages, a
 * floor on how many pages came back, and a cap so a service that never stops
 * fails instead of hanging.
 *
 * <p>The calls are the two the emitter writes for a paginated clause — page one
 * with the page size set, and the same with the input token read from
 * {@link Page#TOKEN_KEY} — over a fake ListQueues that answers from a script and
 * records every request it was sent.
 */
class PaginatedListTest {

    private static final Group GROUP =
            new Group("sqs-gen-queue", "compat/model/scenarios/sqs.json");

    /** A fake ListQueues: pages in order, the last repeating, every request recorded. */
    private static final class Queues {
        final List<ListQueuesResponse> pages = new ArrayList<>();
        final List<ListQueuesRequest> seen = new ArrayList<>();
        RuntimeException failOnSecondCall;

        Queues page(String token, String... urls) {
            ListQueuesResponse.Builder b = ListQueuesResponse.builder().queueUrls(urls);
            if (token != null) {
                b.nextToken(token);
            }
            pages.add(b.build());
            return this;
        }

        Function<Object, Object> send() {
            return request -> {
                int n = seen.size();
                seen.add((ListQueuesRequest) request);
                if (n == 1 && failOnSecondCall != null) {
                    throw failOnSecondCall;
                }
                return pages.get(Math.min(n, pages.size() - 1));
            };
        }

        List<String> sent() {
            List<String> out = new ArrayList<>();
            for (ListQueuesRequest r : seen) {
                out.add("size=" + r.maxResults() + " token=" + r.nextToken());
            }
            return out;
        }
    }

    private static Clause pagedList(Queues q, boolean absent, String want, int minPages) {
        Call first = new Call("ListQueues", "{\"MaxResults\":1,\"QueueNamePrefix\":\"oc-\"}",
                b -> ListQueuesRequest.builder().maxResults(1).queueNamePrefix("oc-").build(),
                q.send());
        Call next = new Call("ListQueues",
                "{\"MaxResults\":1,\"NextToken\":{\"$ref\":\"$pageToken\"},\"QueueNamePrefix\":\"oc-\"}",
                b -> ListQueuesRequest.builder()
                        .maxResults(1)
                        .nextToken(b.string("NextToken", Values.ref("$pageToken")))
                        .queueNamePrefix("oc-")
                        .build(),
                q.send());
        Clause list = absent
                ? Clause.absentFromList(first, "$.QueueUrls", Where.of("$", want))
                : Clause.listContains(first, "$.QueueUrls", Where.of("$", want));
        return list.paged(Page.of(1, minPages, "NextToken", next));
    }

    private static void run(Clause clause) {
        GROUP.runTest(new TestContext("http://127.0.0.1:1", "us-east-1", "oc-test"), "ListQueues",
                new Call("CreateQueue", "{}",
                        b -> CreateQueueRequest.builder().queueName("q").build(),
                        request -> CreateQueueResponse.builder().queueUrl("http://q/created").build()),
                List.of(clause));
    }

    @Test
    void walksEveryPageFeedingTheTokenBack() {
        Queues q = new Queues().page("t1", "http://a").page("t2", "http://b").page(null, "http://q");

        run(pagedList(q, false, "http://q", 0));

        assertEquals(List.of("size=1 token=null", "size=1 token=t1", "size=1 token=t2"), q.sent());
    }

    @Test
    void keepsWalkingAfterTheItemIsFound() {
        Queues q = new Queues().page("t1", "http://q").page(null, "http://b");

        run(pagedList(q, false, "http://q", 0));

        assertEquals(2, q.seen.size(), "the walk always reaches the end");
    }

    @Test
    void anEmptyTokenEndsTheWalk() {
        Queues q = new Queues().page("", "http://q");

        run(pagedList(q, false, "http://q", 0));

        assertEquals(1, q.seen.size());
    }

    @Test
    void aMissSaysHowManyPagesWereWalkedAndQuotesPageOnesParams() {
        Queues q = new Queues().page("t1", "http://a").page("t2", "http://b").page(null, "http://c");

        AssertionError e = assertThrows(AssertionError.class, () -> run(pagedList(q, false, "http://q", 0)));

        String message = e.getMessage();
        assertTrue(message.contains("walked 3 page(s), 3 item(s): "), message);
        assertTrue(message.contains("\"http://a\""), message);
        assertTrue(message.contains("listContains at $.QueueUrls"), message);
        String params = message.substring(0, message.indexOf("expected"));
        assertTrue(params.contains("MaxResults"), message);
        assertFalse(params.contains("NextToken"), "page one's params carry no token: " + message);
    }

    @Test
    void listContainsStillNeedsANonEmptyList() {
        Queues q = new Queues().page(null);

        AssertionError e = assertThrows(AssertionError.class, () -> run(pagedList(q, false, "http://q", 0)));

        assertTrue(e.getMessage().contains("walked 1 page(s), 0 item(s)"), e.getMessage());
    }

    @Test
    void minPagesFailsASinglePageThatHoldsEverything() {
        Queues single = new Queues().page(null, "http://q");

        AssertionError e = assertThrows(AssertionError.class, () -> run(pagedList(single, false, "http://q", 2)));

        assertTrue(e.getMessage().contains("expected at least 2 page(s) at a page size of 1"), e.getMessage());
        assertTrue(e.getMessage().contains("actual walked 1 page(s), 1 item(s)"), e.getMessage());
        assertDoesNotThrow(() -> run(pagedList(new Queues().page("t", "http://a").page(null, "http://q"),
                false, "http://q", 2)));
    }

    @Test
    void aServiceThatNeverStopsFailsAtThePageCap() {
        Queues q = new Queues().page("again", "http://a");

        AssertionError e = assertThrows(AssertionError.class, () -> run(pagedList(q, false, "http://q", 0)));

        assertEquals(Page.MAX_PAGES, q.seen.size());
        assertTrue(e.getMessage().contains("no NextToken on a page within " + Page.MAX_PAGES + " pages"),
                e.getMessage());
        assertTrue(e.getMessage().contains("walked " + Page.MAX_PAGES + " page(s), " + Page.MAX_PAGES
                + " item(s), and the last still returned a NextToken"), e.getMessage());
    }

    @Test
    void absentLooksAtEveryPage() {
        assertDoesNotThrow(() -> run(pagedList(
                new Queues().page("t", "http://a").page(null, "http://b"), true, "http://q", 0)));

        AssertionError e = assertThrows(AssertionError.class, () -> run(pagedList(
                new Queues().page("t", "http://a").page(null, "http://q"), true, "http://q", 0)));

        assertTrue(e.getMessage().contains("walked 2 page(s), 2 item(s): "), e.getMessage());
    }

    @Test
    void aCallFailingOnALaterPageIsACallFailureCarryingThatPagesParams() {
        Queues q = new Queues().page("t1", "http://a");
        q.failOnSecondCall = new IllegalStateException("boom");

        AssertionError e = assertThrows(AssertionError.class, () -> run(pagedList(q, false, "http://q", 0)));

        assertTrue(e.getMessage().contains("call"), e.getMessage());
        assertTrue(e.getMessage().contains("boom"), e.getMessage());
        assertTrue(e.getMessage().contains("\"NextToken\":\"t1\""), e.getMessage());
    }

    @Test
    void insideEventuallyEveryAttemptWalksAgain() {
        Queues q = new Queues()
                .page("t1", "http://a").page(null, "http://b")   // attempt 1: no match
                .page("t1", "http://a").page(null, "http://q");  // attempt 2: match

        run(Clause.eventually(3, 0, pagedList(q, false, "http://q", 0)));

        assertEquals(List.of("size=1 token=null", "size=1 token=t1", "size=1 token=null", "size=1 token=t1"),
                q.sent());
    }
}

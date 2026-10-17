package io.overcast.compat.scenario;

/**
 * The settings for walking a list to its last page
 * ({@code compat/model/README.md} § Paginating a list).
 *
 * <p>The clause's own {@link Call} makes page one, with the page size already
 * set; {@code next} is the same call with the input token bound to
 * {@link #TOKEN_KEY}, which the walk sets to the previous page's token before
 * each later page.
 *
 * @param size        the page size the emitted calls send, for the failure
 *                    message only
 * @param minPages    the fewest pages the service may answer in; 0 for no floor
 * @param outputToken the top-level response member holding the next-page token
 * @param next        makes every page after the first
 */
public record Page(int size, int minPages, String outputToken, Call next) {

    /**
     * The most pages a paginated clause walks. A service that still returns a
     * token after this many fails the clause instead of hanging the suite.
     */
    public static final int MAX_PAGES = 50;

    /**
     * The context path {@code next} reads the token from. It is not a valid
     * context path, so no scenario can export over it.
     */
    public static final String TOKEN_KEY = "$pageToken";

    /** The settings for one walk. */
    public static Page of(int size, int minPages, String outputToken, Call next) {
        return new Page(size, minPages, outputToken, next);
    }
}

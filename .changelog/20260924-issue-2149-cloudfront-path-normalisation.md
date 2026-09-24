* [cloudfront] cache behaviours now match after dot segments are resolved and repeated slashes collapsed, as on AWS.
  `/a/b/..` matches `/a*`, not `/a/b*`, and `/a//b` matches as `/a/b`. The origin still receives the path exactly as sent.
* [cloudfront] a request path that starts with `//` reaches the origin with both slashes.
  it used to lose one, and shared a cache entry with the single-slash path.

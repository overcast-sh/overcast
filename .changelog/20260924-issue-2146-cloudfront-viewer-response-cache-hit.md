* [cloudfront] viewer-response functions now run on a cache hit, as on AWS.
  a hit no longer replays the headers the function set for the viewer that filled the cache; the function runs again for each viewer.

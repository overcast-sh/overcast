* [dynamodb] a TTL transition accepted while Overcast is starting up settles as it should, instead of being cancelled by the start-up re-arm
  the re-arm read the table list once and armed what that snapshot said, so an `UpdateTimeToLive` accepted in between lost the settle for its own deadline
  the table kept a transition marker that outlived its window, and a completed disable never dropped its TTL configuration

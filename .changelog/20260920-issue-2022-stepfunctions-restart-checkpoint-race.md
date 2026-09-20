* [stepfunctions] an execution resumed after a restart is no longer failed as unresumable while it is finishing
    A resumed execution kept the previous process's runner ID for its whole life, so a read arriving as it finished could end it FAILED with States.Runtime.

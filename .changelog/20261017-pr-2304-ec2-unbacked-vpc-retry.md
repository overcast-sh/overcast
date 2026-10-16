* [ec2/rds] A VPC whose Docker network create was refused is retried when a launch asks about it, instead of staying `unbacked` until Docker reappears.
  `CreateDBInstance` in such a VPC failed with `InvalidVPCNetworkStateFault` (`network status=unbacked`) long after the cause, such as an address-pool overlap, had cleared.

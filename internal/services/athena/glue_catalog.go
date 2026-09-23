package athena

import "github.com/overcast-sh/overcast/internal/services/glue"

// InitGlueCatalog wires the Glue Data Catalog Athena reads its databases,
// tables and partitions from — AwsDataCatalog, as Athena calls it. Nothing
// reads it yet: it is the seam the Athena metadata operations (#2065) and the
// query engine (#2066) are built on.
func (s *Service) InitGlueCatalog(c glue.Catalog) { s.catalog = c }

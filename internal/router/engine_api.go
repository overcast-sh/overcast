package router

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/overcast-sh/overcast/internal/awsapi"
	"github.com/overcast-sh/overcast/internal/services/athena"
	"github.com/overcast-sh/overcast/internal/services/s3tables"
)

// icebergCatalog is a service serving S3 Tables' Iceberg REST catalog.
type icebergCatalog interface {
	IcebergRouter() chi.Router
}

// engineAPI is what the Athena engine gateway serves: Glue's JSON API, S3 and
// S3 Tables' Iceberg REST catalog, each behind the API's request middleware
// and each reaching only its own service.
//
// It is not the root router, which dispatches on more than the signing name.
// There an X-Amz-Target, a Query Action (in the body or the query string) or a
// path another service owns carries a request to that service whatever it was
// signed for, which is how a request the gateway admitted as S3 reached IAM,
// and one admitted as Glue reached Lambda (#2214). Here the signing name picks
// the service, and the service's own routing does the rest:
//
//   - Glue is the root router's X-Amz-Target dispatch with Glue as its only
//     dispatcher, so another service's target or Query call gets the 501 the
//     root router gives a modeled operation no enabled service serves;
//   - S3 is S3's own router, which reads every path as a bucket and key;
//   - S3 Tables is its Iceberg catalog at IcebergRoot, and nothing else.
//
// services is the enabled services by name. A service that is not enabled
// leaves its handler nil, which the gateway refuses.
func engineAPI(chain chi.Middlewares, services map[string]Service, s3Router http.Handler, registry *awsapi.Registry) athena.EngineAPI {
	var api athena.EngineAPI
	if _, ok := services["s3"]; ok {
		api.S3 = chain.Handler(s3Router)
	}
	if glue, ok := services["glue"].(TargetDispatcher); ok {
		glueAPI := chi.NewRouter()
		glueAPI.Post("/", (&rootDispatch{registry: registry, targets: []TargetDispatcher{glue}}).targetDispatch)
		api.Glue = chain.Handler(glueAPI)
	}
	if catalog, ok := services["s3tables"].(icebergCatalog); ok {
		icebergAPI := chi.NewRouter()
		icebergAPI.Mount(s3tables.IcebergRoot, catalog.IcebergRouter())
		api.Iceberg = chain.Handler(icebergAPI)
	}
	return api
}

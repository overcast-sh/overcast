//go:build dev

package acm

import "github.com/overcast-sh/overcast/internal/capabilities"

func init() {
	capabilities.Default.Register(
		capabilities.Capability{Service: "acm", Operation: "RequestCertificate", Category: "Certificates", Status: capabilities.StatusSupported, Notes: "Creates a certificate; immediately ISSUED; `DomainName`, each SAN and `ValidationMethod` validated against the modeled constraints; inline `Tags` applied at creation"},
		capabilities.Capability{Service: "acm", Operation: "DescribeCertificate", Category: "Certificates", Status: capabilities.StatusSupported, Notes: "Returns certificate details, including one `DomainValidationOptions` entry per domain — `SUCCESS`, the requested method, and for `DNS` a synthetic CNAME that is stable across calls"},
		capabilities.Capability{Service: "acm", Operation: "ListCertificates", Category: "Certificates", Status: capabilities.StatusPartial, Notes: "Filters on `CertificateStatuses`; `Includes`, `CertificateKeyPairOrigins`, `SortBy`/`SortOrder` and `MaxItems`/`NextToken` are ignored — they select on certificate material Overcast never generates"},
		capabilities.Capability{Service: "acm", Operation: "ListCertificateDomainValidations", Category: "Certificates", Status: capabilities.StatusPartial, Notes: "One synthesized SUCCESS entry per DomainName/SAN, echoing the requested `ValidationMethod`; no `ValidationChallenge` — the DNS record lives on `DescribeCertificate`, and Overcast issues certificates without ever validating them"},
		capabilities.Capability{Service: "acm", Operation: "DeleteCertificate", Category: "Certificates", Status: capabilities.StatusSupported, Notes: "Deletes a certificate by ARN"},
		capabilities.Capability{Service: "acm", Operation: "ListTagsForCertificate", Category: "Tags", Status: capabilities.StatusSupported, Notes: "Lists tags for a certificate"},
		capabilities.Capability{Service: "acm", Operation: "AddTagsToCertificate", Category: "Tags", Status: capabilities.StatusSupported, Notes: "Adds tags to a certificate"},
		capabilities.Capability{Service: "acm", Operation: "RemoveTagsFromCertificate", Category: "Tags", Status: capabilities.StatusSupported, Notes: "Removes tags from a certificate"},
		capabilities.Capability{Service: "acm", Operation: "TagResource", Category: "Tags", Status: capabilities.StatusSupported, Notes: "Modern alias of `AddTagsToCertificate`, addressing the certificate by `ResourceArn`"},
		capabilities.Capability{Service: "acm", Operation: "UntagResource", Category: "Tags", Status: capabilities.StatusSupported, Notes: "Takes `TagKeys`, where `RemoveTagsFromCertificate` takes a `Tags` list"},
		capabilities.Capability{Service: "acm", Operation: "ListTagsForResource", Category: "Tags", Status: capabilities.StatusSupported, Notes: "Modern alias of `ListTagsForCertificate`"},
	)
}

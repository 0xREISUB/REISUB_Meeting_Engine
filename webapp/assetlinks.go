package webapp

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
)

// Defaults for the Android app that verifies invite links (App Links).
const (
	defaultAndroidPackage     = "xyz.reisub.meeting"
	defaultAndroidFingerprint = "62:CE:B3:E9:79:E2:8D:86:FD:73:42:2C:2C:AA:65:30:AD:31:72:31:DA:1F:08:91:B0:AE:6A:36:9A:15:A7:6C"
)

type assetStatement struct {
	Relation []string    `json:"relation"`
	Target   assetTarget `json:"target"`
}

type assetTarget struct {
	Namespace    string   `json:"namespace"`
	PackageName  string   `json:"package_name"`
	Fingerprints []string `json:"sha256_cert_fingerprints"`
}

// AssetLinks returns the handler for GET /.well-known/assetlinks.json, which
// lets Android open https://<this host>/j/... and /invite links directly in
// the app. ANDROID_PACKAGE_NAME and ANDROID_CERT_FINGERPRINTS (comma
// separated SHA-256 signing certificate fingerprints) override the defaults.
// It needs no auth and never redirects.
func AssetLinks() gin.HandlerFunc {
	pkg := strings.TrimSpace(os.Getenv("ANDROID_PACKAGE_NAME"))
	if pkg == "" {
		pkg = defaultAndroidPackage
	}
	var fingerprints []string
	for _, fp := range strings.Split(os.Getenv("ANDROID_CERT_FINGERPRINTS"), ",") {
		if fp = strings.ToUpper(strings.TrimSpace(fp)); fp != "" {
			fingerprints = append(fingerprints, fp)
		}
	}
	if len(fingerprints) == 0 {
		fingerprints = []string{defaultAndroidFingerprint}
	}
	body, _ := json.Marshal([]assetStatement{{
		Relation: []string{"delegate_permission/common.handle_all_urls"},
		Target:   assetTarget{Namespace: "android_app", PackageName: pkg, Fingerprints: fingerprints},
	}})
	return func(c *gin.Context) {
		// Exactly application/json, as Android's verifier expects.
		c.Data(http.StatusOK, "application/json", body)
	}
}

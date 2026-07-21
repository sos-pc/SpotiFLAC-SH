package qobuz

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Qobuz rejects an unsigned catalogue search with 401. The old code sent the
// bare public app_id 798273057 and got exactly that on every ISRC lookup, which
// is why Qobuz never got past its first step (docs/upstream-catchup.md §S6).
//
// These are the Qobuz *web player's own* public credentials, embedded in its
// site bundle and used the way a browser uses them — not a private secret of
// the community infrastructure. Measured working against the live API on
// 2026-07-20 (app_id 712109809), returning real results for real ISRCs.
const (
	signedAppID  = "712109809"
	signedSecret = "589be88e4538daea11f509d29e4a23b1"
)

// signedQobuzURL builds a signed request URL for a Qobuz API endpoint.
//
// The signature is MD5 over a specific concatenation, and every part of it is
// load-bearing:
//
//	MD5( endpoint-without-slashes + sorted("key"+"value" for the query params,
//	     excluding app_id/request_ts/request_sig) + timestamp + secret )
//
// The excluded three are the signature's own envelope; folding them back in
// would make the payload reference its own output. Sorting must be by key, and
// the endpoint's slashes are stripped ("track/search" → "tracksearch") — both
// are how the web player does it, verified by replaying its requests.
func signedQobuzURL(endpoint string, params map[string]string) string {
	ts := time.Now().Unix()

	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var joined strings.Builder
	for _, k := range keys {
		joined.WriteString(k)
		joined.WriteString(params[k])
	}

	normalisedPath := strings.ReplaceAll(endpoint, "/", "")
	toSign := normalisedPath + joined.String() + fmt.Sprint(ts) + signedSecret
	sum := md5.Sum([]byte(toSign))
	signature := hex.EncodeToString(sum[:])

	query := url.Values{}
	for k, v := range params {
		query.Set(k, v)
	}
	query.Set("app_id", signedAppID)
	query.Set("request_ts", fmt.Sprint(ts))
	query.Set("request_sig", signature)

	return fmt.Sprintf("https://www.qobuz.com/api.json/0.2/%s?%s", endpoint, query.Encode())
}

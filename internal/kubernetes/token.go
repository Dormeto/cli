// Package kubernetes is the Kubernetes API of an EKS cluster, without a
// Kubernetes library: plain HTTPS trusting the cluster's certificate
// authority, with an EKS token signed here by hand.
//
// Token ports the router's lambda/src/lib/k8s.mjs (presignUrl, eksToken).
package kubernetes

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
)

func sha256Hex(data string) string {
	sum := sha256.Sum256([]byte(data))
	return hex.EncodeToString(sum[:])
}

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}

// encode is RFC 3986, as SigV4 wants: every byte but the unreserved characters.
func encode(value string) string {
	var b strings.Builder
	for i := 0; i < len(value); i++ {
		c := value[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

type presignRequest struct {
	Method      string // GET when empty
	Host        string
	Path        string // "/" when empty
	Query       [][2]string
	Headers     map[string]string // signed besides host
	Region      string
	Service     string
	Credentials aws.Credentials
	Now         time.Time
	Expires     int
	PayloadHash string // the hash of the empty payload when empty
}

// presignURL is a SigV4 presigned URL (query string authentication). The URL
// keeps botocore's order (the query, then the authentication parameters); the
// signature covers them sorted, as SigV4 says.
func presignURL(r presignRequest) string {
	method, path, payload := r.Method, r.Path, r.PayloadHash
	if method == "" {
		method = "GET"
	}
	if path == "" {
		path = "/"
	}
	if payload == "" {
		payload = sha256Hex("")
	}
	amzDate := r.Now.UTC().Format("20060102T150405Z")
	scope := fmt.Sprintf("%s/%s/%s/aws4_request", amzDate[:8], r.Region, r.Service)
	signed := map[string]string{"host": r.Host}
	for name, value := range r.Headers {
		signed[strings.ToLower(name)] = strings.TrimSpace(value)
	}
	names := make([]string, 0, len(signed))
	for name := range signed {
		names = append(names, name)
	}
	sort.Strings(names)
	raw := append([][2]string{}, r.Query...)
	raw = append(raw,
		[2]string{"X-Amz-Algorithm", "AWS4-HMAC-SHA256"},
		[2]string{"X-Amz-Credential", r.Credentials.AccessKeyID + "/" + scope},
		[2]string{"X-Amz-Date", amzDate},
		[2]string{"X-Amz-Expires", strconv.Itoa(r.Expires)},
		[2]string{"X-Amz-SignedHeaders", strings.Join(names, ";")},
	)
	if r.Credentials.SessionToken != "" {
		raw = append(raw, [2]string{"X-Amz-Security-Token", r.Credentials.SessionToken})
	}
	params := make([][2]string, len(raw))
	for i, p := range raw {
		params[i] = [2]string{encode(p[0]), encode(p[1])}
	}
	sorted := append([][2]string{}, params...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i][0] < sorted[j][0] })
	join := func(ps [][2]string) string {
		parts := make([]string, len(ps))
		for i, p := range ps {
			parts[i] = p[0] + "=" + p[1]
		}
		return strings.Join(parts, "&")
	}
	var headers strings.Builder
	for _, name := range names {
		headers.WriteString(name + ":" + signed[name] + "\n")
	}
	signedHeaders := strings.Join(names, ";")
	canonical := strings.Join([]string{method, path, join(sorted), headers.String(), signedHeaders, payload}, "\n")
	toSign := strings.Join([]string{"AWS4-HMAC-SHA256", amzDate, scope, sha256Hex(canonical)}, "\n")
	key := []byte("AWS4" + r.Credentials.SecretAccessKey)
	for _, part := range []string{amzDate[:8], r.Region, r.Service, "aws4_request"} {
		key = hmacSHA256(key, part)
	}
	signature := hex.EncodeToString(hmacSHA256(key, toSign))
	return fmt.Sprintf("https://%s%s?%s&X-Amz-Signature=%s", r.Host, path, join(params), signature)
}

// Token is the bearer token EKS accepts (what `aws eks get-token` prints): an
// STS GetCallerIdentity URL presigned for 60 seconds with the cluster's name
// signed; EKS takes it for 15 minutes from its date.
func Token(cluster, region string, creds aws.Credentials, now time.Time) string {
	u := presignURL(presignRequest{
		Host:    "sts." + region + ".amazonaws.com",
		Query:   [][2]string{{"Action", "GetCallerIdentity"}, {"Version", "2011-06-15"}},
		Headers: map[string]string{"x-k8s-aws-id": cluster},
		Region:  region, Service: "sts", Credentials: creds, Now: now, Expires: 60,
	})
	return "k8s-aws-v1." + base64.RawURLEncoding.EncodeToString([]byte(u))
}

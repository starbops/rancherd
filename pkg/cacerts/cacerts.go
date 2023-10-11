package cacerts

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io/ioutil"
	"net/http"
	url2 "net/url"
	"time"

	"github.com/rancher/rancherd/pkg/tpm"
	"github.com/rancher/system-agent/pkg/applyinator"
	"github.com/rancher/wrangler/pkg/randomtoken"
)

var insecureClient = &http.Client{
	Timeout: time.Second * 5,
	Transport: &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
		},
	},
}

func Get(server, token, path string) ([]byte, string, error) {
	return get(server, token, path, true)
}

func MachineGet(server, token, path string) ([]byte, string, error) {
	return get(server, token, path, false)
}

func get(server, token, path string, clusterToken bool) ([]byte, string, error) {
	u, err := url2.Parse(server)
	if err != nil {
		return nil, "", err
	}
	u.Path = path

	var (
		isTPM bool
	)
	if !clusterToken {
		isTPM, token, err = tpm.ResolveToken(token)
		if err != nil {
			return nil, "", err
		}
	}

	cacert, caChecksum, err := CACerts(server, token, clusterToken)
	if err != nil {
		return nil, "", err
	}

	if isTPM {
		data, err := tpm.Get(cacert, u.String(), nil)
		return data, caChecksum, err
	}

	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, "", err
	}
	if !clusterToken {
		req.Header.Set("Authorization", "Bearer "+base64.StdEncoding.EncodeToString([]byte(token)))
	}

	var resp *http.Response
	if len(cacert) == 0 {
		resp, err = http.DefaultClient.Do(req)
		if err != nil {
			return nil, "", err
		}
	} else {
		pool := x509.NewCertPool()
		pool.AppendCertsFromPEM(cacert)
		client := http.Client{
			Timeout: 5 * time.Second,
			Transport: &http.Transport{
				Proxy: http.ProxyFromEnvironment,
				TLSClientConfig: &tls.Config{
					RootCAs: pool,
				},
			},
		}
		defer client.CloseIdleConnections()

		resp, err = client.Do(req)
		if err != nil {
			return nil, "", err
		}
	}

	data, err := ioutil.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("%s: %s", data, resp.Status)
	}
	return data, caChecksum, err
}

func CACerts(server, token string, clusterToken bool) ([]byte, string, error) {
	nonce, err := randomtoken.Generate()
	if err != nil {
		return nil, "", err
	}

	url, err := url2.Parse(server)
	if err != nil {
		return nil, "", err
	}

	requestURL := fmt.Sprintf("https://%s/cacerts", url.Host)
	if !clusterToken {
		requestURL = fmt.Sprintf("https://%s/v1-rancheros/cacerts", url.Host)
	}

	if resp, err := http.Get(requestURL); err == nil {
		_, _ = ioutil.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, "", nil
	}

	req, err := http.NewRequest(http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("X-Cattle-Nonce", nonce)
	req.Header.Set("Authorization", "Bearer "+hashBase64([]byte(token)))

	resp, err := insecureClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("insecure cacerts download from %s: %w", requestURL, err)
	}
	defer resp.Body.Close()

	data, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("response %d: %s getting cacerts: %s", resp.StatusCode, resp.Status, data)
	}

	if resp.Header.Get("X-Cattle-Hash") != hash(token, nonce, data) {
		return nil, "", fmt.Errorf("response hash (%s) does not match (%s)",
			resp.Header.Get("X-Cattle-Hash"),
			hash(token, nonce, data))
	}

	if len(data) == 0 {
		return nil, "", nil
	}

	return data, hashHex(data), nil
}

func ToUpdateCACertificatesInstruction() (*applyinator.Instruction, error) {
	cmd := "update-ca-certificates"

	return &applyinator.Instruction{
		Name:       "update-ca-certificates",
		SaveOutput: true,
		Command:    cmd,
	}, nil
}

func ToFile(server, token string) (*applyinator.File, error) {
	cacert, _, err := CACerts(server, token, true)
	if err != nil {
		return nil, err
	}

	return &applyinator.File{
		Content:     base64.StdEncoding.EncodeToString(cacert),
		Path:        "/etc/pki/trust/anchors/additional-ca.pem",
		Permissions: "0644",
	}, nil
}

func hashHex(token []byte) string {
	hash := sha256.Sum256(token)
	return hex.EncodeToString(hash[:])
}

func hashBase64(token []byte) string {
	hash := sha256.Sum256(token)
	return base64.StdEncoding.EncodeToString(hash[:])
}

func hash(token, nonce string, bytes []byte) string {
	digest := hmac.New(sha512.New, []byte(token))
	digest.Write([]byte(nonce))
	digest.Write([]byte{0})
	digest.Write(bytes)
	digest.Write([]byte{0})
	hash := digest.Sum(nil)
	return base64.StdEncoding.EncodeToString(hash)
}

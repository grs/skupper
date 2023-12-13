package vault

import (
	"bytes"
	"context"
	jsonencoding "encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime/serializer/json"
	"k8s.io/client-go/kubernetes/scheme"
)

type VaultConfiguration struct {
	address         string
	secretMountPath string
	appRole         string
	token           string
	authMode        AuthMode
}

func (o *VaultConfiguration) siteUrl(network string, site string) string {
	return fmt.Sprintf(`%s/%s/data/skupper/%s/%s`, o.address, o.secretMountPath, network, site)
}

func (o *VaultConfiguration) authUrl(authPath string) string {
	return fmt.Sprintf(`%s/v1/auth/%s/login`, o.address, authPath)
}

func (o *VaultConfiguration) networkUrl(network string) string {
	return fmt.Sprintf(`%s/%s/metadata/skupper/%s?list=true`, o.address, o.secretMountPath, network)
}

func (o *VaultConfiguration) ReadFromEnv() {
	o.address = os.Getenv("VAULT_ADDRESS")
	o.secretMountPath = os.Getenv("VAULT_MOUNT_PATH")
	if o.secretMountPath == "" {
		o.secretMountPath = "v1/secret"
	}

	authMethod := os.Getenv("VAULT_AUTH_METHOD")
	authPath := os.Getenv("VAULT_AUTH_PATH")
	if authMethod != "" && authPath == "" {
		authPath = authMethod
	}
	if authMethod == "" {
		o.authMode = &DefaultAuth{}
	} else if authMethod == "approle" {
		o.authMode = &AppRoleAuth{
			url: o.authUrl(authPath),
		}
	} else if authMethod == "kubernetes" {
		o.authMode = &KubeAuth{
			url: o.authUrl(authPath),
		}
	} else {
		o.authMode = &UnknownAuth{}
	}
	o.authMode.ReadFromEnv()
}

type AuthMode interface {
	ReadFromEnv()
	GetToken(ctxt context.Context) (string, error)
}

type DefaultAuth struct {
	token string
}

func (o *DefaultAuth) GetToken(ctxt context.Context) (string, error) {
	return o.token, nil
}

func (o *DefaultAuth) ReadFromEnv() {
	o.token = os.Getenv("VAULT_TOKEN")
}

func getKubeToken() (string, error) {
	data, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/token")
	if err != nil {
		return "", err
	}
	return string(data), nil
}

type CachedToken struct {
	token      string
	validUntil time.Time
}

func (o *CachedToken) get() string {
	return o.token
}

func (o *CachedToken) isValid() bool {
	return o.token != "" && time.Now().Before(o.validUntil)
}

func (o *CachedToken) update(response *AuthResponse) {
	o.token = response.Auth.Token
	o.validUntil = time.Now().Add(time.Duration(response.Auth.LeaseDuration) * time.Millisecond)
	log.Printf("Got new token, valid until %s", o.validUntil.Format(time.UnixDate))
}

type KubeAuth struct {
	url        string
	role       string
	token      CachedToken
}

func (o *KubeAuth) GetToken(ctxt context.Context) (string, error) {
	if o.token.isValid() {
		return o.token.get(), nil
	}
	kubeToken, err := getKubeToken()
	if err != nil {
		return "", err
	}
	requestBody := &KubernetesAuthRequest{
		Token: kubeToken,
		Role:  o.role,
	}
	responseBody := &AuthResponse{}
	err = request(ctxt, "POST", o.url, nil, requestBody, responseBody)
	if err != nil {
		return "", err
	}
	o.token.update(responseBody)
	return o.token.get(), nil
}

func (o *KubeAuth) ReadFromEnv() {
	o.role = os.Getenv("VAULT_KUBE_ROLE")
}

type AppRoleAuth struct {
	url      string
	roleId   string
	secretId string
	token    CachedToken
}

func (o *AppRoleAuth) GetToken(ctxt context.Context) (string, error) {
	if o.token.isValid() {
		return o.token.get(), nil
	}
	requestBody := &AppRoleAuthRequest{
		RoleId:    o.roleId,
		SecretId:  o.secretId,
	}
	responseBody := &AuthResponse{}
	err := request(ctxt, "POST", o.url, nil, requestBody, responseBody)
	if err != nil {
		return "", err
	}
	o.token.update(responseBody)
	return o.token.get(), nil
}

func (o *AppRoleAuth) ReadFromEnv() {
	o.roleId = os.Getenv("VAULT_ROLE_ID")
	o.secretId = os.Getenv("VAULT_SECRET_ID")
}

type UnknownAuth struct {
	method string
}

func (o *UnknownAuth) GetToken(ctxt context.Context) (string, error) {
	return "", fmt.Errorf("Unknown auth method for vault %s", o.method)
}

func (o *UnknownAuth) ReadFromEnv() {
	o.method = os.Getenv("VAULT_AUTH_METHOD")
}

type Metadata struct {
	Created   string `json:"created_time"`
	Deleted   string `json:"deletion_time"`
	Destroyed bool      `json:"destroyed"`
	Version   uint      `json:"version"`
}

type SecretData struct {
	Token string `json:"options"`
}

type UpdateSecretRequest struct {
	Options map[string]interface{} `json:"options"`
	Data    SecretData `json:"data"`
}

type UpdateSecretResponse struct {
	Data Metadata `json:"data"`
}

type GetSecretResponse struct {
	Data struct {
		Data     SecretData `json:"data"`
		Metadata Metadata   `json:"metadata"`
	} `json:"data"`
}

type GetSubKeysResponse struct {
	Data struct {
		Keys     []string `json:"keys"`
	} `json:"data"`
}

type AppRoleAuthRequest struct {
	RoleId   string `json:"role_id"`
	SecretId string `json:"secret_id"`
}

type KubernetesAuthRequest struct {
	Token string `json:"jwt"`
	Role  string `json:"role"`
}

type AuthResponse struct {
	Auth struct {
		LeaseDuration int    `json:"lease_duration"`
		Token         string `json:"client_token"`
	} `json:"auth"`
}

type Client interface {
	PublishToken(ctx context.Context, network string, siteId string, secret *corev1.Secret) error
	GetTokens(ctx context.Context, network string, siteId string) ([]*corev1.Secret, error)
	DeleteToken(ctx context.Context, network string, siteId string) error
}

func NewClient() Client {
	client := &VaultConfiguration{}
	client.ReadFromEnv()
	return client
}

func request(ctx context.Context, method, url string, headers map[string]string, requestBody interface{}, responseBody interface{}) error {
	client := &http.Client{}
	var body io.Reader
	if requestBody != nil {
		data, err := jsonencoding.MarshalIndent(requestBody, "", "    ")
		if err != nil {
			return err
		}
		body = strings.NewReader(string(data))
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return err
	}
	for key, value := range headers {
		req.Header.Add(key, value)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	responseData, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("%s to %s failed with %s: %s", method, url, resp.StatusCode, responseData)
	}
	err = jsonencoding.Unmarshal(responseData, responseBody)
	if err != nil {
		return err
	}
	return nil
}

func (c *VaultConfiguration) request(ctx context.Context, method, url string, requestBody interface{}, responseBody interface{}) error {
	token, err := c.authMode.GetToken(ctx)
	if err != nil {
		return err
	}
	return request(ctx, method, url, map[string]string{"X-Vault-Token": token}, requestBody, responseBody)
}

func (c *VaultConfiguration) PublishToken(ctx context.Context, network string, siteId string, secret *corev1.Secret) error {
	token, err := toString(secret)
	if err != nil {
		return err
	}
	url := c.siteUrl(network, siteId)
	request := &UpdateSecretRequest {
		Data: SecretData{
			Token: token,
		},
	}
	response := &UpdateSecretResponse{}
	err = c.request(ctx, "POST", url, request, response)
	if err != nil {
		return err
	}
	return nil
}

func (c *VaultConfiguration) getToken(ctx context.Context, network string, siteId string) (*corev1.Secret, error) {
	url := c.siteUrl(network, siteId)
	response:= &GetSecretResponse{}
	err := c.request(ctx, "GET", url, nil, response)
	if err != nil {
		return nil, err
	}
	return fromString(response.Data.Data.Token)
}

func (c *VaultConfiguration) GetTokens(ctx context.Context, network string, siteId string) ([]*corev1.Secret, error) {
	url := c.networkUrl(network)
	response := &GetSubKeysResponse{}
	err := c.request(ctx, "GET", url, nil, response)
	if err != nil {
		return nil, err
	}
	var tokens []*corev1.Secret
	for _, key := range response.Data.Keys {
		if key != siteId {
			token, err := c.getToken(ctx, network, key)
			if err != nil {
				return nil, err
			}
			tokens = append(tokens, token)
		}
	}
	return tokens, nil
}

func (c *VaultConfiguration) DeleteToken(ctx context.Context, network string, siteId string) error {
	response := map[string]interface{}{}
	url := c.siteUrl(network, siteId)
	err := c.request(ctx, "DELETE", url, nil, response)
	if err != nil {
		return err
	}
	return nil
}

func toString(secret *corev1.Secret) (string, error) {
	var buffer bytes.Buffer
	s := json.NewSerializerWithOptions(json.DefaultMetaFactory, scheme.Scheme, scheme.Scheme, json.SerializerOptions{true, false, false})
	err := s.Encode(secret, &buffer)
	if err != nil {
		return "", err
	}
	return buffer.String(), nil
}

func fromString(input string) (*corev1.Secret, error) {
	s := json.NewSerializerWithOptions(json.DefaultMetaFactory, scheme.Scheme, scheme.Scheme, json.SerializerOptions{true, false, false})
	var secret corev1.Secret
	_, _, err := s.Decode([]byte(input), nil, &secret)
	return &secret, err
}

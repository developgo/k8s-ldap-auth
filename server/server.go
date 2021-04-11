package server

import (
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/gorilla/mux"

	auth "k8s.io/api/authentication/v1"
	machinery "k8s.io/apimachinery/pkg/apis/meta/v1"
	client "k8s.io/client-go/pkg/apis/clientauthentication/v1beta1"

	"bouchaud.org/legion/kubernetes/k8s-ldap-auth/ldap"
	"bouchaud.org/legion/kubernetes/k8s-ldap-auth/types"
)

const ContentTypeHeader = "Content-Type"
const ContentTypeJSON = "application/json"

type Instance struct {
	l *ldap.Ldap
	m []mux.MiddlewareFunc
	k *rsa.PrivateKey
}

func NewInstance(opts ...Option) (*Instance, error) {
	key, err := types.Key()
	if err != nil {
		return nil, err
	}

	s := &Instance{
		m: []mux.MiddlewareFunc{},
		k: key,
	}

	for _, opt := range opts {
		opt(s)
	}

	r := mux.NewRouter()

	r.HandleFunc("/auth", s.authenticate()).Methods("POST")
	r.HandleFunc("/token", s.validate()).Methods("POST")
	r.Use(s.m...)

	http.Handle("/", r)

	return s, nil
}

func (s *Instance) Start(addr string) error {
	if err := http.ListenAndServe(addr, nil); err != http.ErrServerClosed {
		return fmt.Errorf("Server stopped unexpectedly, %w", err)
	}

	return nil
}

func writeError(res http.ResponseWriter, s *ServerError) {
	res.WriteHeader(s.s)
	res.Write([]byte(s.e.Error()))
}

func (s *Instance) authenticate() http.HandlerFunc {
	return func(res http.ResponseWriter, req *http.Request) {
		if req.Header.Get(ContentTypeHeader) != ContentTypeJSON {
			writeError(res, ErrNotAcceptable)
			return
		}

		decoder := json.NewDecoder(req.Body)
		var credentials types.Credentials
		if err := decoder.Decode(&credentials); err != nil {
			writeError(res, ErrDecodeFailed)
			return
		}
		defer req.Body.Close()

		if !credentials.IsValid() {
			writeError(res, ErrMalformedCredentials)
			return
		}

		user, err := s.l.Search(credentials.Username, credentials.Password)
		if err != nil {
			writeError(res, ErrUnauthorized)
			return
		}

		data, err := json.Marshal(user)
		if err != nil {
			writeError(res, ErrServerError)
			return
		}

		token := types.NewToken(data)
		tokenData, err := token.Payload(nil)
		if err != nil {
			writeError(res, ErrServerError)
			return
		}

		tokenExp, err := token.Expiration()
		if err != nil {
			writeError(res, ErrServerError)
			return
		}

		ec := client.ExecCredential{
			Status: &client.ExecCredentialStatus{
				Token: string(tokenData),
				ExpirationTimestamp: &machinery.Time{
					Time: tokenExp,
				},
			},
		}

		res.Header().Set(ContentTypeHeader, ContentTypeJSON)
		json.NewEncoder(res).Encode(ec)
	}
}

func (s *Instance) validate() http.HandlerFunc {
	return func(res http.ResponseWriter, req *http.Request) {
		if req.Header.Get(ContentTypeHeader) != ContentTypeJSON {
			writeError(res, ErrNotAcceptable)
			return
		}

		decoder := json.NewDecoder(req.Body)
		var tr auth.TokenReview
		if err := decoder.Decode(&tr); err != nil {
			writeError(res, ErrDecodeFailed)
			return
		}
		defer req.Body.Close()

		token, err := types.Parse([]byte(tr.Spec.Token), nil)
		if err != nil {
			writeError(res, ErrMalformedToken)
			return
		}

		if token.IsValid() == false {
			tr.Status.Authenticated = false
		} else {
			user, err := token.GetUser()
			if err != nil {
				writeError(res, ErrServerError)
				return
			}

			tr.Status.Authenticated = true
			tr.Status.User = auth.UserInfo{
				Username: user.Uid,
				UID:      user.DN,
				Groups:   user.Groups,
			}
		}

		res.Header().Set(ContentTypeHeader, ContentTypeJSON)
		json.NewEncoder(res).Encode(tr)
	}
}

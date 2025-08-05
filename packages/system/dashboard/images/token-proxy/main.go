package main

import (
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/securecookie"
)

/* ----------------------------- flags ------------------------------------ */

var (
	upstream, httpAddr, proxyPrefix string
	cookieName, cookieSecretB64     string
	cookieSecure                    bool
	cookieRefresh                   time.Duration
	tokenCheckURL                   string
)

func init() {
	flag.StringVar(&upstream, "upstream", "", "Upstream URL to proxy to (required)")
	flag.StringVar(&httpAddr, "http-address", "0.0.0.0:8000", "Listen address")
	flag.StringVar(&proxyPrefix, "proxy-prefix", "/oauth2", "URL prefix for control endpoints")

	flag.StringVar(&cookieName, "cookie-name", "_oauth2_proxy_0", "Cookie name")
	flag.StringVar(&cookieSecretB64, "cookie-secret", "", "Base64-encoded cookie secret")
	flag.BoolVar(&cookieSecure, "cookie-secure", false, "Set Secure flag on cookie")
	flag.DurationVar(&cookieRefresh, "cookie-refresh", 0, "Cookie refresh interval (e.g. 1h)")
	flag.StringVar(&tokenCheckURL, "token-check-url", "", "URL for external token validation")
}

/* ----------------------------- templates -------------------------------- */

var loginTmpl = template.Must(template.New("login").Parse(`
<!doctype html><html><head><title>Login</title></head>
<body>
	<h2>Enter ServiceAccount / OIDC token</h2>
	{{if .Err}}<p style="color:red">{{.Err}}</p>{{end}}
	<form method="POST" action="{{.Action}}">
		<input style="width:420px" name="token" placeholder="Paste token" autofocus/>
		<button type="submit">Login</button>
	</form>
</body></html>`))

/* ----------------------------- helpers ---------------------------------- */

func decodeJWT(raw string) jwt.MapClaims {
	tkn, _ := jwt.Parse(raw, nil)
	if c, ok := tkn.Claims.(jwt.MapClaims); ok {
		return c
	}
	return jwt.MapClaims{}
}

func externalTokenCheck(raw string) error {
	if tokenCheckURL == "" {
		return nil
	}
	req, _ := http.NewRequest(http.MethodGet, tokenCheckURL, nil)
	req.Header.Set("Authorization", "Bearer "+raw)
	cli := &http.Client{Timeout: 5 * time.Second}
	resp, err := cli.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}

func encodeSession(sc *securecookie.SecureCookie, token string, exp, issued int64) (string, error) {
	v := map[string]interface{}{
		"access_token": token,
		"expires":      exp,
		"issued":       issued,
	}
	return sc.Encode(cookieName, v)
}

/* ----------------------------- main ------------------------------------- */

func main() {
	flag.Parse()
	if upstream == "" {
		log.Fatal("--upstream is required")
	}
	upURL, err := url.Parse(upstream)
	if err != nil {
		log.Fatalf("invalid upstream url: %v", err)
	}

	if cookieSecretB64 == "" {
		cookieSecretB64 = os.Getenv("COOKIE_SECRET")
	}
	if cookieSecretB64 == "" {
		log.Fatal("--cookie-secret or $COOKIE_SECRET is required")
	}
	secret, err := base64.StdEncoding.DecodeString(cookieSecretB64)
	if err != nil {
		log.Fatalf("cookie-secret: %v", err)
	}
	sc := securecookie.New(secret, nil)

	// control paths
	signIn := path.Join(proxyPrefix, "sign_in")
	signOut := path.Join(proxyPrefix, "sign_out")
	userInfo := path.Join(proxyPrefix, "userinfo")

	proxy := httputil.NewSingleHostReverseProxy(upURL)

	/* ------------------------- /sign_in ---------------------------------- */

	http.HandleFunc(signIn, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			_ = loginTmpl.Execute(w, struct {
				Action string
				Err    string
			}{Action: signIn})
		case http.MethodPost:
			token := strings.TrimSpace(r.FormValue("token"))
			if token == "" {
				_ = loginTmpl.Execute(w, struct {
					Action string
					Err    string
				}{Action: signIn, Err: "Token required"})
				return
			}
			if err := externalTokenCheck(token); err != nil {
				_ = loginTmpl.Execute(w, struct {
					Action string
					Err    string
				}{Action: signIn, Err: "Invalid token"})
				return
			}

			exp := time.Now().Add(24 * time.Hour).Unix()
			claims := decodeJWT(token)
			if v, ok := claims["exp"].(float64); ok {
				exp = int64(v)
			}
			session, _ := encodeSession(sc, token, exp, time.Now().Unix())
			http.SetCookie(w, &http.Cookie{
				Name:     cookieName,
				Value:    session,
				Path:     "/",
				Expires:  time.Unix(exp, 0),
				Secure:   cookieSecure,
				HttpOnly: true,
				SameSite: http.SameSiteLaxMode,
			})
			http.Redirect(w, r, "/", http.StatusSeeOther)
		}
	})

	/* ------------------------- /sign_out --------------------------------- */

	http.HandleFunc(signOut, func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{
			Name:     cookieName,
			Value:    "",
			Path:     "/",
			MaxAge:   -1,
			Secure:   cookieSecure,
			HttpOnly: true,
		})
		http.Redirect(w, r, signIn, http.StatusSeeOther)
	})

	/* ------------------------- /userinfo --------------------------------- */

	http.HandleFunc(userInfo, func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(cookieName)
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var sess map[string]interface{}
		if err := sc.Decode(cookieName, c.Value, &sess); err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		token, _ := sess["access_token"].(string)
		claims := decodeJWT(token)

		out := map[string]interface{}{
			"token":                 token,
			"sub":                   claims["sub"],
			"email":                 claims["email"],
			"preferred_username":    claims["preferred_username"],
			"groups":                claims["groups"],
			"expires":               sess["expires"],
			"issued":                sess["issued"],
			"cookie_refresh_enable": cookieRefresh > 0,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	})

	/* ----------------------------- proxy --------------------------------- */

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(cookieName)
		if err != nil {
			http.Redirect(w, r, signIn, http.StatusFound)
			return
		}
		var sess map[string]interface{}
		if err := sc.Decode(cookieName, c.Value, &sess); err != nil {
			http.Redirect(w, r, signIn, http.StatusFound)
			return
		}
		token, _ := sess["access_token"].(string)
		if token == "" {
			http.Redirect(w, r, signIn, http.StatusFound)
			return
		}

		// cookie refresh
		if cookieRefresh > 0 {
			if issued, ok := sess["issued"].(float64); ok {
				if time.Since(time.Unix(int64(issued), 0)) > cookieRefresh {
					enc, _ := encodeSession(sc, token, int64(sess["expires"].(float64)), time.Now().Unix())
					http.SetCookie(w, &http.Cookie{
						Name:     cookieName,
						Value:    enc,
						Path:     "/",
						Expires:  time.Unix(int64(sess["expires"].(float64)), 0),
						Secure:   cookieSecure,
						HttpOnly: true,
						SameSite: http.SameSiteLaxMode,
					})
				}
			}
		}

		r.Header.Set("Authorization", "Bearer "+token)
		proxy.ServeHTTP(w, r)
	})

	log.Printf("Listening on %s → %s (control prefix %s)", httpAddr, upURL, proxyPrefix)
	if err := http.ListenAndServe(httpAddr, nil); err != nil {
		log.Fatal(err)
	}
}

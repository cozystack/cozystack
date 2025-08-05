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
<!doctype html>
<html lang="en">
<head>
	<meta charset="UTF-8">
	<title>Login</title>
	<style>
		body {
			margin: 0;
			height: 100vh;
			display: flex;
			align-items: center;
			justify-content: center;
			background: #f4f6f8;
			font-family: Arial, sans-serif;
		}
		.card {
			background: #fff;
			padding: 2rem;
			border-radius: 12px;
			box-shadow: 0 4px 20px rgba(0,0,0,0.1);
			width: 400px;
			text-align: center;
		}
		h2 {
			margin-bottom: 1rem;
			color: #333;
		}
		input {
			width: 100%;
			padding: 0.8rem;
			margin-bottom: 1rem;
			border: 1px solid #ccc;
			border-radius: 8px;
			font-size: 1rem;
			transition: border 0.3s;
		}
		input:focus {
			outline: none;
			border-color: #4a90e2;
		}
		button {
			width: 100%;
			padding: 0.8rem;
			background: #4a90e2;
			color: white;
			font-size: 1rem;
			font-weight: bold;
			border: none;
			border-radius: 8px;
			cursor: pointer;
			transition: background 0.3s;
		}
		button:hover {
			background: #357ABD;
		}
		.error {
			color: #e74c3c;
			margin-bottom: 1rem;
		}
	</style>
</head>
<body>
	<div class="card">
		<h2>Kubernetes API Token</h2>
		{{if .Err}}<p class="error">{{.Err}}</p>{{end}}
		<form method="POST" action="{{.Action}}">
			<input type="text" name="token" placeholder="Paste token here" autofocus />
			<button type="submit">Login</button>
		</form>
	</div>
</body>
</html>`))

/* ----------------------------- helpers ---------------------------------- */

func decodeJWT(raw string) jwt.MapClaims {
	if raw == "" {
		return jwt.MapClaims{}
	}
	tkn, _, err := new(jwt.Parser).ParseUnverified(raw, jwt.MapClaims{})
	if err != nil || tkn == nil {
		return jwt.MapClaims{}
	}
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
	if sc != nil {
		return sc.Encode(cookieName, v)
	}
	return token, nil
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
	var sc *securecookie.SecureCookie
	if cookieSecretB64 != "" {
		secret, err := base64.StdEncoding.DecodeString(cookieSecretB64)
		if err != nil {
			log.Fatalf("cookie-secret: %v", err)
		}
		sc = securecookie.New(secret, nil)
	} else {
		log.Println("warning: no cookie-secret provided, cookies will be stored unsigned")
	}

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
		var token string
		var sess map[string]interface{}
		if sc != nil {
			if err := sc.Decode(cookieName, c.Value, &sess); err != nil {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			token, _ = sess["access_token"].(string)
		} else {
			token = c.Value
			sess = map[string]interface{}{
				"expires": time.Now().Add(24 * time.Hour).Unix(),
				"issued":  time.Now().Unix(),
			}
		}
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
		var token string
		var sess map[string]interface{}
		if sc != nil {
			if err := sc.Decode(cookieName, c.Value, &sess); err != nil {
				http.Redirect(w, r, signIn, http.StatusFound)
				return
			}
			token, _ = sess["access_token"].(string)
		} else {
			token = c.Value
			sess = map[string]interface{}{
				"expires": time.Now().Add(24 * time.Hour).Unix(),
				"issued":  time.Now().Unix(),
			}
		}
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

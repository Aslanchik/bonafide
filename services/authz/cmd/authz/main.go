// authz is the bonafide data-plane binary: an OIDC discovery surface,
// a JWKS endpoint, and the RFC 8693 token-exchange endpoint. main is
// fail-closed startup glue (CLAUDE.md "Safety constraints"): any
// missing key, missing trust file, missing policy file, or unwritable
// audit path produces a non-zero exit before the listener binds.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"bonafide.local/services/authz/internal/audit"
	"bonafide.local/services/authz/internal/config"
	"bonafide.local/services/authz/internal/exchange"
	"bonafide.local/services/authz/internal/httputil"
	"bonafide.local/services/authz/internal/keys"
	"bonafide.local/services/authz/internal/policy"
	"bonafide.local/services/authz/internal/trust"
)

func main() {
	if err := run(); err != nil {
		slog.Error("event=authz_startup_failed", "err", err.Error())
		os.Exit(1)
	}
}

func run() error {
	settings, err := config.Load()
	if err != nil {
		return err
	}

	signer, err := keys.LoadSigner(settings.SigningKeyPath)
	if err != nil {
		return err
	}

	verifier, err := trust.LoadYAMLTrust(settings.ActorTrustPath, settings.Issuer)
	if err != nil {
		return err
	}

	gate, err := policy.LoadMapGate(settings.PolicyPath)
	if err != nil {
		return err
	}

	emitter, err := audit.NewFileEmitter(settings.AuditPath)
	if err != nil {
		return err
	}

	handlers := httputil.Handlers{
		OIDCDiscovery: discoveryHandler(settings.Issuer),
		JWKS:          jwksHandler(signer),
		TokenExchange: exchange.Handler(gate, verifier, signer, emitter, exchange.Settings{
			Issuer:              settings.Issuer,
			TaskTokenTTLSeconds: settings.TaskTokenTTLSeconds,
		}),
		Health: healthHandler(),
	}

	srv := &http.Server{
		Addr:              settings.Listen,
		Handler:           httputil.NewRouter(handlers),
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		slog.Info("event=authz_listening", "addr", settings.Listen, "issuer", settings.Issuer)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		slog.Info("event=authz_shutdown_started")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		if err := emitter.Close(); err != nil {
			slog.Error("event=audit_close_failed", "err", err.Error())
		}
		return nil
	case err := <-errCh:
		_ = emitter.Close()
		return err
	}
}

// discoveryHandler serves the OIDC discovery document at
// /.well-known/openid-configuration. The shape is standard (CONTRACT.md
// §11) — no bonafide-specific extensions. jwks_uri is the only field
// downstream tooling reads in M1; the rest are advertised so generic
// OIDC libraries (e.g. zitadel/oidc consumers in later slices) can
// auto-discover the surface.
func discoveryHandler(issuer string) http.HandlerFunc {
	doc := map[string]any{
		"issuer":                                issuer,
		"jwks_uri":                              issuer + "/.well-known/jwks.json",
		"token_endpoint":                        issuer + "/token",
		"grant_types_supported":                 []string{"urn:ietf:params:oauth:grant-type:token-exchange"},
		"token_endpoint_auth_methods_supported": []string{"none"},
		"id_token_signing_alg_values_supported": []string{"EdDSA"},
	}
	payload, err := json.Marshal(doc)
	if err != nil {
		// Marshal of a literal map[string]any with primitive values
		// cannot fail; treat a failure as a programming error.
		panic("discoveryHandler: marshal: " + err.Error())
	}
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}
}

// jwksHandler serves /.well-known/jwks.json with the signer's published
// JWKS (CONTRACT.md §11: Ed25519 keys only).
func jwksHandler(signer *keys.Signer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		doc, err := signer.JWKSDocument()
		if err != nil {
			httputil.WriteServerError(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(doc)
	}
}

// healthHandler is the liveness probe. CONTRACT.md §11 says the shape
// is standard; the smoke harness and the docker-compose healthcheck
// both look for a 200.
func healthHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}
}

//go:build ignore

// Runs only disposable local Docker resources. It has no deployment transport,
// production credentials or remote host access. Run from tests/e2e's module.
package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/jwt"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: business-container-smoke IMAGE")
		os.Exit(2)
	}
	if err := run(os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, "business container certification:", err)
		os.Exit(1)
	}
}
func run(image string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	containers := []string{}
	docker := func(args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "docker", args...)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err := cmd.Run()
		if err != nil {
			return "", fmt.Errorf("docker %s failed: %s", args[0], strings.TrimSpace(stderr.String()))
		}
		return strings.TrimSpace(stdout.String()), nil
	}
	defer func() {
		for i := len(containers) - 1; i >= 0; i-- {
			_, _ = exec.Command("docker", "rm", "-f", containers[i]).CombinedOutput()
		}
	}()
	launch := func(args ...string) (string, error) {
		id, err := docker(append([]string{"run", "-d"}, args...)...)
		if err == nil {
			containers = append(containers, id)
		}
		return id, err
	}
	free := func() (string, error) {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return "", err
		}
		defer l.Close()
		return l.Addr().String(), nil
	}
	pgAddr, err := free()
	if err != nil {
		return err
	}
	natsAddr, err := free()
	if err != nil {
		return err
	}
	apiAddr, err := free()
	if err != nil {
		return err
	}
	workerAddr, err := free()
	if err != nil {
		return err
	}
	_, pgPort, _ := net.SplitHostPort(pgAddr)
	_, natsPort, _ := net.SplitHostPort(natsAddr)
	password := security.RandomToken(32)
	pgContainer, err := launch("-p", "127.0.0.1:"+pgPort+":5432", "-e", "POSTGRES_PASSWORD="+password, "postgres:16-alpine")
	if err != nil {
		return err
	}
	broker, err := launch("-p", "127.0.0.1:"+natsPort+":4222", "nats:2.11-alpine", "-js", "-sd", "/data")
	if err != nil {
		return err
	}
	dsn := "postgres://postgres:" + password + "@" + pgAddr + "/postgres?sslmode=disable"
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return errors.New("disposable database pool failed")
	}
	defer pool.Close()
	wait := func(label string, fn func() bool) error {
		deadline := time.Now().Add(45 * time.Second)
		for time.Now().Before(deadline) && ctx.Err() == nil {
			if fn() {
				return nil
			}
			time.Sleep(200 * time.Millisecond)
		}
		return fmt.Errorf("%s did not become ready", label)
	}
	if err = wait("disposable PostgreSQL", func() bool {
		call, done := context.WithTimeout(ctx, time.Second)
		defer done()
		return pool.Ping(call) == nil
	}); err != nil {
		return err
	}
	applySQL := func(path string) error {
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()
		database := "postgres"
		if strings.Contains(path, "runtime-grants") {
			database = "business_operations_db"
		}
		cmd := exec.CommandContext(ctx, "docker", "exec", "-i", pgContainer, "psql", "-U", "postgres", "-d", database, "-v", "ON_ERROR_STOP=1")
		cmd.Stdin = file
		if _, err = cmd.CombinedOutput(); err != nil {
			return errors.New("disposable provisioning/grant script failed")
		}
		return nil
	}
	for i := 0; i < 2; i++ {
		if err = applySQL("../../scripts/database/business-operations-provision.sql"); err != nil {
			return err
		}
	}
	pool.Close()
	dsn = "postgres://postgres:" + password + "@" + pgAddr + "/business_operations_db?sslmode=disable"
	pool, err = pgxpool.New(ctx, dsn)
	if err != nil {
		return errors.New("disposable service database connection failed")
	}
	defer pool.Close()
	key := func(kid string) jwt.SigningKey {
		_, private, _ := ed25519.GenerateKey(rand.Reader)
		return jwt.SigningKey{KID: kid, PrivateKey: private}
	}
	gateway, business, access := key("smoke-gateway"), key("smoke-business"), key("smoke-access")
	registryJSON, _ := json.Marshal([]map[string]any{{"service": platform.APIGateway, "keys": []map[string]string{{"kid": gateway.KID, "public_key": security.EncodeEd25519PublicKey(gateway.PublicKey())}}}, {"service": platform.BusinessOperationsService, "keys": []map[string]string{{"kid": business.KID, "public_key": security.EncodeEd25519PublicKey(business.PublicKey())}}}})
	registry, err := serviceauth.ParseRegistry(registryJSON)
	if err != nil {
		return err
	}
	notificationVerifier, err := serviceauth.NewVerifier(serviceauth.VerifierConfig{Self: platform.NotificationService, Registry: registry})
	if err != nil {
		return err
	}
	var mu sync.Mutex
	acceptances := map[string]ids.ID{}
	provider := httptest.NewServer(serviceauth.Authenticate(notificationVerifier, "Authorization")(serviceauth.RequireCaller(platform.BusinessOperationsService)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/internal/v1/notifications" {
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body) != nil {
			http.Error(w, "invalid body", 400)
			return
		}
		idem := r.Header.Get("Idempotency-Key")
		if idem == "" {
			http.Error(w, "missing idempotency", 400)
			return
		}
		mu.Lock()
		id, exists := acceptances[idem]
		if !exists {
			id = ids.New()
			acceptances[idem] = id
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(202)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"notification_id": id, "status": "QUEUED"}})
	}))))
	defer provider.Close()
	directory, err := os.MkdirTemp("", "cf-business-smoke-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(directory)
	writeEnv := func(name string, env map[string]string) (string, error) {
		var b strings.Builder
		for k, v := range env {
			fmt.Fprintf(&b, "%s=%s\n", k, v)
		}
		path := filepath.Join(directory, name)
		return path, os.WriteFile(path, []byte(b.String()), 0600)
	}
	migrationEnv, err := writeEnv("migration.env", map[string]string{"DATABASE_URL": dsn + "&options=-c%20role%3Dcf_business_owner"})
	if err != nil {
		return err
	}
	for _, command := range []string{"up", "up", "status", "version"} {
		out, err := docker("run", "--rm", "--network", "host", "--env-file", migrationEnv, image, "/app/migrate", command)
		if err != nil {
			return fmt.Errorf("migration %s failed", command)
		}
		if command == "version" && out != "4" {
			return fmt.Errorf("unexpected migration version %s", out)
		}
	}
	if err = applySQL("../../scripts/database/business-operations-runtime-grants.sql"); err != nil {
		return err
	}
	// Ephemeral test password, generated in memory, never printed or committed.
	if _, err = pool.Exec(ctx, "ALTER ROLE cf_business_runtime PASSWORD '"+password+"'"); err != nil {
		return errors.New("disposable runtime credential setup failed")
	}
	runtimeDSN := "postgres://cf_business_runtime:" + password + "@" + pgAddr + "/business_operations_db?sslmode=disable"
	runtimePool, err := pgxpool.New(ctx, runtimeDSN)
	if err != nil {
		return errors.New("runtime pool failed")
	}
	defer runtimePool.Close()
	if _, err = runtimePool.Exec(ctx, `CREATE TABLE public.forbidden_runtime_ddl(id int)`); err == nil {
		return errors.New("runtime unexpectedly has DDL authority")
	}
	if _, err = runtimePool.Exec(ctx, `UPDATE audit_events SET action='forbidden' WHERE false`); err == nil {
		return errors.New("runtime unexpectedly can mutate audit history")
	}
	protectedKey := make([]byte, 32)
	if _, err = rand.Read(protectedKey); err != nil {
		return err
	}
	base := map[string]string{"APP_ENV": "test", "LOG_LEVEL": "warn", "DATABASE_URL": runtimeDSN, "NATS_URL": "nats://" + natsAddr, "NATS_SUBJECT_PREFIX": "smoke", "OUTBOX_POLL_INTERVAL": "100ms", "SERVICE_AUTH_KEY_ID": business.KID, "SERVICE_AUTH_PRIVATE_KEY": security.EncodeEd25519Seed(business.PrivateKey), "SERVICE_AUTH_TRUSTED_KEYS": string(registryJSON), "BUSINESS_MFA_KEY_ID": "smoke-mfa", "BUSINESS_MFA_KEY": base64.StdEncoding.EncodeToString(protectedKey), "NOTIFICATION_SERVICE_URL": provider.URL, "VERIFICATION_SERVICE_URL": provider.URL, "SHUTDOWN_TIMEOUT": "10s"}
	workerEnv := map[string]string{}
	for k, v := range base {
		workerEnv[k] = v
	}
	workerEnv["HTTP_ADDR"] = workerAddr
	apiEnv := map[string]string{}
	for k, v := range base {
		apiEnv[k] = v
	}
	apiEnv["HTTP_ADDR"] = apiAddr
	apiEnv["BUSINESS_ACCESS_KEY_ID"] = access.KID
	apiEnv["BUSINESS_ACCESS_PRIVATE_KEY"] = security.EncodeEd25519Seed(access.PrivateKey)
	apiEnv["BUSINESS_CODE_PEPPER"] = security.RandomToken(48)
	workerFile, err := writeEnv("worker.env", workerEnv)
	if err != nil {
		return err
	}
	apiFile, err := writeEnv("api.env", apiEnv)
	if err != nil {
		return err
	}
	worker, err := launch("--network", "host", "--env-file", workerFile, image, "/app/worker")
	if err != nil {
		return err
	}
	api, err := launch("--network", "host", "--env-file", apiFile, image, "/app/api")
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 3 * time.Second}
	ready := func(address string) bool {
		r, err := client.Get("http://" + address + "/ready")
		if err != nil {
			return false
		}
		defer r.Body.Close()
		return r.StatusCode == 200
	}
	if err = wait("API container", func() bool { return ready(apiAddr) }); err != nil {
		return err
	}
	if err = wait("worker container", func() bool { return ready(workerAddr) }); err != nil {
		return err
	}
	for _, probe := range []struct{ id, bin string }{{api, "/app/api"}, {worker, "/app/worker"}} {
		if _, err = docker("exec", probe.id, probe.bin, "healthcheck"); err != nil {
			return err
		}
	}
	redisAddr, err := free()
	if err != nil {
		return err
	}
	_, redisPort, _ := net.SplitHostPort(redisAddr)
	if _, err = launch("-p", "127.0.0.1:"+redisPort+":6379", "redis:7.4-alpine"); err != nil {
		return err
	}
	gatewayAddr, err := free()
	if err != nil {
		return err
	}
	gatewayBinary := filepath.Join(directory, "gateway")
	buildGateway := exec.CommandContext(ctx, "go", "build", "-o", gatewayBinary, "./cmd/api")
	buildGateway.Dir = "../../gateway/api-gateway"
	buildGateway.Env = append(os.Environ(), "GOWORK=off", "GOTOOLCHAIN=local")
	if out, err := buildGateway.CombinedOutput(); err != nil {
		return fmt.Errorf("gateway build failed: %s", out)
	}
	issuers, _ := json.Marshal([]map[string]any{{"issuer": platform.BusinessOperationsService, "domain": platform.DomainBusinessOperations, "keys": []map[string]string{{"kid": access.KID, "public_key": security.EncodeEd25519PublicKey(access.PublicKey())}}}})
	gatewayLog, err := os.OpenFile(filepath.Join(directory, "gateway.log"), os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer gatewayLog.Close()
	gatewayProcess := exec.CommandContext(ctx, gatewayBinary)
	gatewayProcess.Env = []string{"PATH=" + os.Getenv("PATH"), "APP_ENV=test", "LOG_LEVEL=warn", "HTTP_ADDR=" + gatewayAddr, "REDIS_ADDR=" + redisAddr, "GATEWAY_TRUSTED_ISSUERS=" + string(issuers), "SERVICE_AUTH_KEY_ID=" + gateway.KID, "SERVICE_AUTH_PRIVATE_KEY=" + security.EncodeEd25519Seed(gateway.PrivateKey), "GATEWAY_UPSTREAM_BUSINESS_OPERATIONS_SERVICE_URL=http://" + apiAddr}
	gatewayProcess.Stdout = gatewayLog
	gatewayProcess.Stderr = gatewayLog
	if err = gatewayProcess.Start(); err != nil {
		return errors.New("gateway process startup failed")
	}
	gatewayDone := make(chan error, 1)
	go func() { gatewayDone <- gatewayProcess.Wait() }()
	gatewayStopped := false
	stopGateway := func() error {
		if gatewayStopped {
			return nil
		}
		gatewayStopped = true
		_ = gatewayProcess.Process.Signal(syscall.SIGTERM)
		select {
		case err := <-gatewayDone:
			return err
		case <-time.After(15 * time.Second):
			_ = gatewayProcess.Process.Kill()
			<-gatewayDone
			return errors.New("gateway did not stop gracefully")
		}
	}
	defer func() { _ = stopGateway() }()
	if err = wait("actual Gateway process", func() bool { return ready(gatewayAddr) }); err != nil {
		return err
	}
	var lastEmail string
	var lastOrg ids.ID
	register := func() error {
		lastEmail = "smoke-" + ids.New().String() + "@example.test"
		body, _ := json.Marshal(map[string]any{"email": lastEmail, "password": "correct horse battery smoke", "display_name": "Smoke account", "organisation_name": "Disposable Business"})
		req, _ := http.NewRequestWithContext(ctx, "POST", "http://"+gatewayAddr+"/api/v1/business/auth/register", bytes.NewReader(body))
		req.Header.Set("Idempotency-Key", ids.New().String())
		req.Header.Set("Content-Type", "application/json")
		response, err := client.Do(req)
		if err != nil {
			return errors.New("registration container request failed")
		}
		defer response.Body.Close()
		if response.StatusCode != 201 {
			return fmt.Errorf("container registration status %d", response.StatusCode)
		}
		var registration struct {
			Data struct {
				Organisation ids.ID `json:"organisation_id"`
			} `json:"data"`
		}
		if err := json.NewDecoder(response.Body).Decode(&registration); err != nil {
			return err
		}
		lastOrg = registration.Data.Organisation
		if lastOrg.IsZero() {
			return errors.New("registration omitted organisation identifier")
		}
		return nil
	}
	if err = register(); err != nil {
		return err
	}
	loginBody, _ := json.Marshal(map[string]string{"email": lastEmail, "password": "correct horse battery smoke", "organisation_id": lastOrg.String()})
	loginRequest, _ := http.NewRequestWithContext(ctx, "POST", "http://"+gatewayAddr+"/api/v1/business/auth/login", bytes.NewReader(loginBody))
	loginRequest.Header.Set("Content-Type", "application/json")
	loginResponse, err := client.Do(loginRequest)
	if err != nil {
		return errors.New("gateway login failed")
	}
	var login struct {
		Data struct {
			Tokens struct {
				AccessToken string `json:"access_token"`
			} `json:"tokens"`
		} `json:"data"`
	}
	decodeErr := json.NewDecoder(loginResponse.Body).Decode(&login)
	loginResponse.Body.Close()
	if decodeErr != nil || loginResponse.StatusCode != 200 || login.Data.Tokens.AccessToken == "" {
		return errors.New("gateway login did not return validated business credentials")
	}
	for _, path := range []string{"/api/v1/business/organisation", "/api/v1/business-operations/organisation", "/api/v1/business/sessions"} {
		request, _ := http.NewRequestWithContext(ctx, "GET", "http://"+gatewayAddr+path, nil)
		request.Header.Set("Authorization", "Bearer "+login.Data.Tokens.AccessToken)
		request.Header.Set("X-Organisation-ID", ids.New().String())
		response, err := client.Do(request)
		if err != nil {
			return errors.New("gateway business request failed")
		}
		response.Body.Close()
		if response.StatusCode != 200 {
			return fmt.Errorf("gateway business route %s status %d", path, response.StatusCode)
		}
	}
	for _, path := range []string{"/internal/v1/business/accounts", "/api/v1/business/unknown"} {
		request, _ := http.NewRequestWithContext(ctx, "GET", "http://"+gatewayAddr+path, nil)
		request.Header.Set("Authorization", "Bearer "+login.Data.Tokens.AccessToken)
		response, err := client.Do(request)
		if err != nil {
			return err
		}
		response.Body.Close()
		if response.StatusCode != 404 {
			return fmt.Errorf("gateway route isolation status %d", response.StatusCode)
		}
	}
	if err = wait("outbox and authenticated Notification acceptance", func() bool {
		var n int
		err := pool.QueryRow(ctx, `SELECT count(*) FROM notification_acceptances`).Scan(&n)
		return err == nil && n >= 1
	}); err != nil {
		return err
	}
	if _, err = docker("stop", "--time", "10", broker); err != nil {
		return err
	}
	if err = register(); err != nil {
		return err
	}
	var pending int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE published_at IS NULL AND dead_at IS NULL`).Scan(&pending); err != nil || pending == 0 {
		return errors.New("broker outage did not retain committed outbox work")
	}
	if _, err = docker("start", broker); err != nil {
		return err
	}
	if err = wait("broker recovery", func() bool {
		var pending, n int
		err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM outbox_events WHERE published_at IS NULL AND dead_at IS NULL),(SELECT count(*) FROM notification_acceptances)`).Scan(&pending, &n)
		return err == nil && pending == 0 && n >= 2
	}); err != nil {
		return err
	}
	stop := func(id string) error {
		if _, err := docker("stop", "--time", "20", id); err != nil {
			return err
		}
		code, err := docker("inspect", "--format", "{{.State.ExitCode}}", id)
		if err != nil {
			return err
		}
		if code != "0" {
			return fmt.Errorf("container did not exit gracefully: %s", code)
		}
		return nil
	}
	if err = stopGateway(); err != nil {
		return fmt.Errorf("gateway shutdown: %w", err)
	}
	if err = stop(worker); err != nil {
		return err
	}
	if _, err = docker("start", worker); err != nil {
		return err
	}
	if err = wait("restarted worker", func() bool { return ready(workerAddr) }); err != nil {
		return err
	}
	if err = stop(worker); err != nil {
		return err
	}
	if err = stop(api); err != nil {
		return err
	}
	fmt.Println("PASS: actual Gateway process with Redis, signed user/service identity and canonical/compatibility/internal routing; real PostgreSQL/NATS containers; fresh/repeated migrations/version; non-root API/worker startup; registration commit; authenticated notification acceptance; broker outage/recovery; worker restart; health/readiness; graceful SIGTERM. Provider is a signed contract fake, not staging delivery.")
	return nil
}

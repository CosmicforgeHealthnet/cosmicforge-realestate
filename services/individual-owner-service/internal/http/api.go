package http

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/authz"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/dbx"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/schema"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/usecases"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	httpx "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/idempotency"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/observability"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/pagination"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/requestcontext"
	"go.opentelemetry.io/otel/attribute"
)

// Route registry for the Sections 6–13 domain API. Every route is declared
// once with its contract metadata; the same declaration mounts the handler
// and generates the OpenAPI documents and Postman folders
// (cmd/apidocgen), so implementation and documentation cannot drift.

// Audience selects who may call a route.
type Audience int

const (
	// Owner: /api/v1/owner via the gateway with an owner access token.
	Owner Audience = iota
	// Service: /internal/v1/owner with a service JWT (capability-checked).
	Service
)

// Param documents a query parameter.
type Param struct {
	Name, Doc string
	Enum      []string
	Required  bool
}

// Route is one documented operation.
type Route struct {
	Method, Path, Op, Tag, Summary, Doc string
	Audience                            Audience
	Idempotent, IfMatch, Upsert, List   bool
	Status                              int
	Req, Resp                           reflect.Type
	Query                               []Param
	Errors                              []int
	Captures                            map[string]string // Postman variable -> JSON path in the response
	StepUp                              bool
	handler                             func(h *Handler, rt Route) http.HandlerFunc
}

// Internal marks the route as a service-to-service contract.
func (r Route) Internal() Route { r.Audience = Service; return r }

// Describe adds a long description.
func (r Route) Describe(doc string) Route { r.Doc = doc; return r }

// Q documents query parameters.
func (r Route) Q(p ...Param) Route { r.Query = append(r.Query, p...); return r }

// Errs documents additional error statuses.
func (r Route) Errs(codes ...int) Route { r.Errors = append(r.Errors, codes...); return r }

// Capture records a Postman variable from the response (e.g. "property_id" <- "data.id").
func (r Route) Capture(variable, path string) Route {
	m := map[string]string{}
	for k, v := range r.Captures {
		m[k] = v
	}
	m[variable] = path
	r.Captures = m
	return r
}

// SteppedUp documents a step-up (recent re-authentication) requirement.
func (r Route) SteppedUp() Route { r.StepUp = true; return r }

// FullPath is the mounted path.
func (r Route) FullPath() string {
	if r.Audience == Service {
		return "/internal/v1/owner" + r.Path
	}
	return "/api/v1/owner" + r.Path
}

// Call is the per-request context handed to route functions.
type Call struct {
	S       *usecases.Service
	Ctx     context.Context
	R       *http.Request
	Subj    authz.Subject
	Version int64 // If-Match (0 when absent and not required)
	ids     map[string]ids.ID
}

// ID returns a path identifier (validated before the route function runs).
func (c *Call) ID(name string) ids.ID { return c.ids[name] }

// Query returns a trimmed query parameter.
func (c *Call) Query(name string) string { return strings.TrimSpace(c.R.URL.Query().Get(name)) }

// QueryID parses an optional UUIDv7 query parameter.
func (c *Call) QueryID(name string) (ids.ID, error) {
	v := c.Query(name)
	if v == "" {
		return ids.Nil, nil
	}
	id, err := ids.ParseV7(v)
	if err != nil {
		return ids.Nil, apperrors.InvalidRequest(name+" must be a UUIDv7.", apperrors.Detail{Field: name, Code: "INVALID", Message: name + " must be a UUIDv7"})
	}
	return id, nil
}

// DownstreamKey derives a deterministic idempotency key for a downstream
// call from this request's Idempotency-Key, so a retried command repeats the
// same downstream request instead of duplicating it.
func (c *Call) DownstreamKey(purpose string) string {
	rc := requestcontext.From(c.Ctx)
	ct, cid, _ := idempotency.CallerKey(c.Ctx)
	sum := sha256.Sum256([]byte(ct + "|" + cid + "|" + rc.IdempotencyKey + "|" + purpose))
	return "io-" + hex.EncodeToString(sum[:20])
}

// Key returns a DownstreamKey generator bound to this call.
func (c *Call) Key() func(string) string { return c.DownstreamKey }

// Page is the parsed cursor request.
type Page struct {
	Cursor string
	Limit  int
}

var pathParam = regexp.MustCompile(`\{([a-z_]+)\}`)

func (h *Handler) prepare(w http.ResponseWriter, r *http.Request, rt Route) (*Call, error) {
	c := &Call{S: h.svc, Ctx: r.Context(), R: r, ids: map[string]ids.ID{}}
	if rt.Audience == Service {
		c.Subj = serviceSubject(r)
	} else {
		c.Subj = ownerSubject(r)
	}
	for _, m := range pathParam.FindAllStringSubmatch(rt.Path, -1) {
		id, err := ids.ParseV7(chi.URLParam(r, m[1]))
		if err != nil {
			return nil, apperrors.NotFound("NOT_FOUND", "The requested resource was not found.")
		}
		c.ids[m[1]] = id
	}
	if rt.IfMatch {
		v, err := parseIfMatch(r, rt.Upsert)
		if err != nil {
			return nil, err
		}
		c.Version = v
	}
	return c, nil
}

func parseIfMatch(r *http.Request, allowZero bool) (int64, error) {
	v := strings.TrimSpace(r.Header.Get("If-Match"))
	if v == "" {
		return 0, apperrors.New(http.StatusPreconditionRequired, "PRECONDITION_REQUIRED", "If-Match with the current version is required.")
	}
	n, err := strconv.ParseInt(strings.Trim(v, `"`), 10, 64)
	if err != nil || n < 0 || (n == 0 && !allowZero) {
		return 0, apperrors.InvalidRequest(`If-Match must be the quoted resource version, e.g. "3".`)
	}
	return n, nil
}

func decodeBody[Req any](w http.ResponseWriter, r *http.Request) ([]byte, *Req, error) {
	var body []byte
	if r.ContentLength != 0 || r.Header.Get("Content-Type") != "" {
		var err error
		if body, err = httpx.ReadBody(w, r, maxCommandBody); err != nil {
			return nil, nil, err
		}
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		body = []byte("{}")
	}
	req := new(Req)
	if err := httpx.DecodeJSONBytes(body, req); err != nil {
		return nil, nil, err
	}
	if err := schema.Validate(req); err != nil {
		return nil, nil, err
	}
	return body, req, nil
}

// versionOf returns the Version field of a response DTO for the ETag header.
func versionOf(v any) (int64, bool) {
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return 0, false
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return 0, false
	}
	f := rv.FieldByName("Version")
	if !f.IsValid() || f.Kind() != reflect.Int64 {
		return 0, false
	}
	return f.Int(), true
}

func setETag(w http.ResponseWriter, v any) {
	if n, ok := versionOf(v); ok && n > 0 {
		w.Header().Set("ETag", etag(n))
	}
}

// Empty is the request type of commands without a body.
type Empty struct{}

// Command declares an idempotent command (POST/DELETE): Idempotency-Key is
// required, the body is validated, and fn runs inside the same transaction
// that stores the idempotent response.
func Command[Req, Resp any](method, path, op, tag, summary string, status int, fn func(c *Call, req *Req) (Resp, error)) Route {
	return CommandPre[Req, Resp, struct{}](method, path, op, tag, summary, status, nil,
		func(c *Call, req *Req, _ struct{}) (Resp, error) { return fn(c, req) })
}

// CommandPre is Command with a pre-transaction phase for downstream calls
// (never made inside a database transaction). pre runs after the
// idempotency key is claimed; its result feeds fn.
func CommandPre[Req, Resp, P any](method, path, op, tag, summary string, status int,
	pre func(c *Call, req *Req) (P, error), fn func(c *Call, req *Req, p P) (Resp, error)) Route {
	rt := Route{Method: method, Path: path, Op: op, Tag: tag, Summary: summary, Status: status, Idempotent: true,
		Req: reflect.TypeFor[Req](), Resp: reflect.TypeFor[Resp]()}
	rt.handler = func(h *Handler, rt Route) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			c, err := h.prepare(w, r, rt)
			var body []byte
			var req *Req
			if err == nil {
				body, req, err = decodeBody[Req](w, r)
			}
			if err != nil {
				writeOperationError(w, r, err)
				return
			}
			var p P
			var preFn func(ctx context.Context) error
			if pre != nil {
				preFn = func(ctx context.Context) error {
					c.Ctx = ctx
					var err error
					p, err = pre(c, req)
					return err
				}
			}
			h.idem.ServeCommand(w, r, "owner."+rt.Op, body, preFn, func(ctx context.Context, tx pgx.Tx) (idempotency.Result, error) {
				c.Ctx = dbx.WithTx(ctx, tx)
				resp, err := fn(c, req, p)
				if err != nil {
					return idempotency.Result{}, err
				}
				setETag(w, resp)
				res := idempotency.Result{Status: rt.Status, Body: httpx.DataEnvelope{Data: resp}, ResourceType: rt.Tag}
				if rt.Status == http.StatusNoContent {
					res.Body = nil
				}
				return res, nil
			})
		}
	}
	return rt
}

// Update declares a PATCH/PUT guarded by If-Match (optimistic concurrency);
// replays are naturally safe because a stale version is rejected.
// upsert allows If-Match: "0" to create a singleton that does not exist yet.
func Update[Req, Resp any](method, path, op, tag, summary string, upsert bool, fn func(c *Call, req *Req) (Resp, error)) Route {
	rt := Route{Method: method, Path: path, Op: op, Tag: tag, Summary: summary, Status: http.StatusOK, IfMatch: true, Upsert: upsert,
		Req: reflect.TypeFor[Req](), Resp: reflect.TypeFor[Resp]()}
	rt.handler = func(h *Handler, rt Route) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			c, err := h.prepare(w, r, rt)
			var req *Req
			if err == nil {
				_, req, err = decodeBody[Req](w, r)
			}
			var resp Resp
			if err == nil {
				// The use case opens its own transaction (after any
				// downstream reads, which never run inside a transaction).
				resp, err = fn(c, req)
			}
			if err != nil {
				writeOperationError(w, r, err)
				return
			}
			setETag(w, resp)
			httpx.WriteData(w, http.StatusOK, resp)
		}
	}
	return rt
}

// Query declares a side-effect-free POST (previews/calculations): the body
// is validated, no Idempotency-Key is needed and nothing is persisted.
func Query[Req, Resp any](path, op, tag, summary string, fn func(c *Call, req *Req) (Resp, error)) Route {
	rt := Route{Method: http.MethodPost, Path: path, Op: op, Tag: tag, Summary: summary, Status: http.StatusOK,
		Req: reflect.TypeFor[Req](), Resp: reflect.TypeFor[Resp]()}
	rt.handler = func(h *Handler, rt Route) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			c, err := h.prepare(w, r, rt)
			var req *Req
			if err == nil {
				_, req, err = decodeBody[Req](w, r)
			}
			var resp Resp
			if err == nil {
				resp, err = fn(c, req)
			}
			if err != nil {
				writeOperationError(w, r, err)
				return
			}
			httpx.WriteData(w, http.StatusOK, resp)
		}
	}
	return rt
}

// Get declares a read returning one object.
func Get[Resp any](path, op, tag, summary string, fn func(c *Call) (Resp, error)) Route {
	rt := Route{Method: http.MethodGet, Path: path, Op: op, Tag: tag, Summary: summary, Status: http.StatusOK, Resp: reflect.TypeFor[Resp]()}
	rt.handler = func(h *Handler, rt Route) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			c, err := h.prepare(w, r, rt)
			var resp Resp
			if err == nil {
				resp, err = fn(c)
			}
			if err != nil {
				writeOperationError(w, r, err)
				return
			}
			setETag(w, resp)
			httpx.WriteData(w, http.StatusOK, resp)
		}
	}
	return rt
}

// List declares a cursor-paginated collection read.
func List[Item any](path, op, tag, summary string, fn func(c *Call, p Page) (pagination.Response[Item], error)) Route {
	rt := Route{Method: http.MethodGet, Path: path, Op: op, Tag: tag, Summary: summary, Status: http.StatusOK, List: true, Resp: reflect.TypeFor[Item]()}
	rt.handler = func(h *Handler, rt Route) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			c, err := h.prepare(w, r, rt)
			var p Page
			if err == nil {
				p.Cursor = r.URL.Query().Get("cursor")
				p.Limit, err = pagination.Limit(r.URL.Query().Get("limit"), pagination.DefaultLimit, pagination.MaxLimit)
			}
			var resp pagination.Response[Item]
			if err == nil {
				resp, err = fn(c, p)
			}
			if err != nil {
				writeOperationError(w, r, err)
				return
			}
			httpx.WriteJSON(w, http.StatusOK, resp)
		}
	}
	return rt
}

// mountRoutes mounts the registry routes of one audience.
func (h *Handler) mountRoutes(r chi.Router, aud Audience) {
	for _, rt := range DomainRoutes() {
		if rt.Audience == aud {
			r.Method(rt.Method, rt.Path, rt.handler(h, rt))
		}
	}
}

func writeOperationError(w http.ResponseWriter, r *http.Request, err error) {
	code := apperrors.CodeOf(err)
	switch code {
	case "CALENDAR_CONFLICT", "DATES_UNAVAILABLE":
		observability.NewCounter("individual-owner-service", "owner_booking_conflicts_total", "Rejected overlapping booking/calendar commands").Add(r.Context(), 1, attribute.String("code", code))
	}
	httpx.WriteError(w, r, err)
}

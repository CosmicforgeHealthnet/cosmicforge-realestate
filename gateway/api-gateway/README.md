# API Gateway

External entry point for the Real Estate platform.

Responsibilities:
- route public/client API traffic;
- validate/propagate user authentication context;
- enforce edge rate limits and request policies;
- attach request/trace context;
- route to the correct domain/shared service.

The gateway owns **no business database** and must not become a business-domain authority.

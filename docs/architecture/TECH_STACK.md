# Technology Stack

## Backend
Go is the primary backend language for all domain services and the API gateway.

## Mobile
Flutter and Dart are used for Android/iOS applications.

## Web
Next.js, React and TypeScript are used for the public marketplace, business portals and admin backoffice.

## Data
PostgreSQL is the primary transactional database. Redis is used for cache, rate limiting, short-lived coordination and other explicitly justified ephemeral workloads.

## Communication
CF Meet APIs provide messaging, voice calls and video calls. This repository must not duplicate CF Meet's RTC/communications platform.

## Payments
Paystack is an external payment provider. The platform payment service remains the authoritative business/payment state machine and reconciliation layer.

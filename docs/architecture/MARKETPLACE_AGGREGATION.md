# Marketplace Aggregation

Marketplace is the public discovery and aggregation layer.

Originating domains remain authoritative:
- Individual Owner owns individual-owner records/listings.
- Business Operations owns business/development/inventory records.
- Property Manager owns management mandates/operational records.
- Artisan owns artisan/service-provider records.
- Professional Services owns professional-provider records.

Marketplace stores searchable/public projections, ranking/discovery state, favorites, recent views and recommendations.

A publication flow may use events to update marketplace projections. Before a transaction that requires current authoritative state, Marketplace may call the originating service's internal API.

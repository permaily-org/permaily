---
title: Packs
description: Community-maintained, versioned rules you can ask for by name, and the catalogue of them.
---

A pack is a community-maintained, versioned rule, such as `travel.bookings`. Ask for one by name instead of inventing a filter, and people see the same familiar name everywhere.

## What's in a pack

A pack is a public, versioned file. `match` decides which emails qualify, and `fields` decides what comes back as [typed data](/docs/fields-types-tokens/).

```yaml
# packs/travel.bookings.yaml
id: travel.bookings
version: 3
description: Flight, hotel, rail and car bookings
match:
  any:
    - schema_org:
        - FlightReservation
        - LodgingReservation
        - TrainReservation
        - RentalCarReservation
    - from_domain: [iberia.com, booking.com, airbnb.com]
      subject_any: [booking, reservation, itinerary, confirmation]
fields:
  is_cancellation: boolean
  cancellation_notes: string?
  refunded: boolean
```

## Catalogue

| Pack | Version | Matches |
| --- | --- | --- |
| `travel.bookings` | 3 | Flight, hotel, rail and car bookings |

## Versions

You ask for a pack with its version, such as `travel.bookings@3`, and the access pins it.

- A new version that widens what matches needs fresh consent.
- A new version that narrows what matches can apply automatically.

## How packs change

Packs work like shared ad-blocker filter lists: adding a sender is a pull request to `packs/` in the [public repository](https://github.com/permaily-org/permaily). Changes are reviewed and checked automatically, and the checks reject broad matches such as a whole free-mail domain.

## When a pack doesn't cover what you need

Write a [rule](/docs/rules/) in your app's request, or propose a change to the pack. Everything still goes through the same consent screen and preview.

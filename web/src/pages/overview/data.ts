/** The travel.bookings version 4 update that the Review action on Overview opens. */
export const packUpdate = {
  attentionId: 'travel-bookings-v4',
  pack: 'travel.bookings',
  from: 3,
  to: 4,
  senders: [
    { domain: 'vueling.com', matches: 4 },
    { domain: 'flixbus.com', matches: 2 },
  ],
  connectionIds: ['trip-app', 'claude'],
  maintainers: 3,
}

# public_one_time_share_invite

Public-default regression for one-time share invites:

- Alice attaches a public-default service, stops attach, restarts attach, then mints a fresh invite.
- Bob connects successfully with the fresh invite.
- A fresh second client attempt with the same invite is denied server-side as already redeemed.

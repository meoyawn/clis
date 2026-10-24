These synthetic fixtures model Ozon's response shapes. They contain fake
products, buyers, account data and continuation state. No browser credentials,
private HAR bodies or real account identifiers are included. Keep new fixtures
synthetic; never copy raw browser responses into this directory.

Widget IDs are deliberately arbitrary. Search uses the layout's named producer
and state reference to separate results from recommendation grids. Other parsers
select widget type prefixes, since Ozon changes the numeric layout IDs. Wire
fixtures use JSON-encoded strings inside `widgetStates`; tests also exercise
direct object states.

Private captures can be replayed locally with `OZON_HAR_DIR`. They are not needed
for the normal test suite.

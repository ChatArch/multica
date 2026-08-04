-- Nothing writes to these tables until the dual-write PR lands, so dropping
-- them loses no delivered notification: inbox_item is still the only source of
-- truth at this point.
DROP TABLE IF EXISTS inbox_group;
DROP TABLE IF EXISTS inbox_event;

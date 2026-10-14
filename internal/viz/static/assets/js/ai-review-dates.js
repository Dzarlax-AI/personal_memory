// Date input values are local calendar days. Convert their boundaries to UTC
// instants only when building the history API request.
(function (root, factory) {
  const api = factory();
  if (typeof module === 'object' && module.exports) module.exports = api;
  else root.aiReviewDates = api;
})(typeof globalThis !== 'undefined' ? globalThis : this, function () {
  function localDateValue(date) {
    return `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, '0')}-${String(date.getDate()).padStart(2, '0')}`;
  }

  function localMidnight(value) {
    const match = /^(\d{4})-(\d{2})-(\d{2})$/.exec(value || '');
    if (!match) throw new RangeError('Expected a YYYY-MM-DD calendar date.');
    const [, yearText, monthText, dayText] = match;
    const date = new Date(0);
    date.setFullYear(Number(yearText), Number(monthText) - 1, Number(dayText));
    date.setHours(0, 0, 0, 0);
    if (date.getFullYear() !== Number(yearText) || date.getMonth() !== Number(monthText) - 1 || date.getDate() !== Number(dayText)) {
      throw new RangeError('Invalid calendar date.');
    }
    return date;
  }

  function defaultLast14Days(now = new Date()) {
    const end = new Date(now);
    end.setHours(0, 0, 0, 0);
    const start = new Date(end);
    start.setDate(start.getDate() - 13);
    return [localDateValue(start), localDateValue(end)];
  }

  function localDateStart(value) {
    return localMidnight(value).toISOString();
  }

  function localDateEnd(value) {
    const endExclusive = localMidnight(value);
    endExclusive.setDate(endExclusive.getDate() + 1);
    return new Date(endExclusive.getTime() - 1000).toISOString().replace(/\.\d{3}Z$/, '.999999999Z');
  }

  function localDateRange(from, to) {
    const start = localMidnight(from);
    const endExclusive = localMidnight(to);
    if (start > endExclusive) throw new RangeError('Start date must not be after end date.');
    return {
      from: start.toISOString(),
      to: localDateEnd(to),
    };
  }

  return { localDateValue, defaultLast14Days, localDateStart, localDateEnd, localDateRange };
});

// Which service groups start ticked on the services screen.
//
// First run offers the defaults. Returning to the screen (change services, or
// after "Update now") keeps what the user saved: a default-on group they had
// turned off stays off, so an update does not silently pull it (B-234).
function initialServiceSelection(groups, saved, fromChange) {
  const keepSaved = fromChange && Array.isArray(saved) && saved.length > 0;
  const selected = new Set();
  groups.forEach((g) => {
    const wanted = g.required || (keepSaved ? saved.includes(g.id) : g.defaultOn);
    if (wanted && (!g.locked || g.required)) selected.add(g.id);
  });
  return selected;
}

if (typeof module !== "undefined") module.exports = { initialServiceSelection };

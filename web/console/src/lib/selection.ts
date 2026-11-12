import { useCallback, useMemo, useState } from "react";

/** Selection of rows by ID, restricted to the rows currently listed. */
export function useSelection(ids: string[]) {
  const [selected, setSelected] = useState<ReadonlySet<string>>(new Set());

  // Rows that disappeared (replayed, deleted) leave the selection.
  const current = useMemo(() => new Set(ids.filter((id) => selected.has(id))), [ids, selected]);

  const toggle = useCallback((id: string) => {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  }, []);

  const allSelected = ids.length > 0 && current.size === ids.length;
  const toggleAll = useCallback(() => {
    setSelected(allSelected ? new Set() : new Set(ids));
  }, [allSelected, ids]);

  const clear = useCallback(() => setSelected(new Set()), []);

  return { selected: current, ids: [...current], toggle, toggleAll, allSelected, clear };
}

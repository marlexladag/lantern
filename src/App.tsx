import { useCallback, useEffect, useState } from 'react';
import './styles/tokens.css';
import './styles/chrome.css';
import './App.css';
import { EngineStatus } from './components/EngineStatus';
import {
  Sidebar,
  ADD_CONNECTION_EVENT,
  CONNECTIONS_CHANGED_EVENT,
  type TableSelection,
} from './components/Sidebar';
import { ConnectionDialog } from './components/ConnectionDialog';
import { ResultGrid } from './components/ResultGrid';

export default function App() {
  const [dialogOpen, setDialogOpen] = useState(false);
  /**
   * The table whose rows fill the main pane, or null before anything has
   * been chosen.
   *
   * Held here rather than in Sidebar because two components need it and
   * neither contains the other: the sidebar marks the row, the main pane
   * shows the data. This is their nearest common ancestor, so it is the one
   * place they can agree.
   */
  const [selection, setSelection] = useState<TableSelection | null>(null);

  const openDialog = useCallback(() => setDialogOpen(true), []);
  const closeDialog = useCallback(() => setDialogOpen(false), []);

  // Replaces the selection outright, with no check for "same table again".
  // Selecting the table already on screen has to be free, and it is: the
  // three values below are strings, so ResultGrid's own
  // `useCallback(..., [sessionId, database, table])` keeps the same identity
  // and its reset-and-refetch effect never re-runs. A comparison here would
  // be a second mechanism for something one layer down already guarantees —
  // and one that cannot be told apart from working by accident.
  const selectTable = useCallback((sessionId: string, database: string, table: string) => {
    setSelection({ sessionId, database, table });
  }, []);

  // Sidebar's own empty-state affordance asks for the dialog this way,
  // rather than through a prop, so Sidebar stays mountable on its own
  // (`<Sidebar />`, no wiring required) and this is the only place that
  // owns a ConnectionDialog instance.
  useEffect(() => {
    window.addEventListener(ADD_CONNECTION_EVENT, openDialog);
    return () => window.removeEventListener(ADD_CONNECTION_EVENT, openDialog);
  }, [openDialog]);

  return (
    <div className="app-shell">
      <header className="app-titlebar">
        <h1>Lantern</h1>
        <EngineStatus />
        <span className="app-titlebar-spacer" />
        <button type="button" className="app-add-connection" onClick={openDialog}>
          Add connection
        </button>
      </header>
      <div className="app-body">
        <Sidebar onSelectTable={selectTable} selectedTable={selection} />
        <main className="app-main">
          {selection ? (
            /*
              Deliberately NOT keyed by the table. ResultGrid already resets
              itself when its props name a different table, and bumps a
              generation so a page still in flight for the old one cannot
              land in the new one's rows — both covered by its own tests.
              A `key` would remount instead, quietly retiring the mechanism
              those tests exercise while looking identical on screen.
            */
            <ResultGrid
              sessionId={selection.sessionId}
              database={selection.database}
              table={selection.table}
            />
          ) : (
            <span className="app-main-empty">Select a table to see its data.</span>
          )}
        </main>
      </div>
      <ConnectionDialog
        open={dialogOpen}
        onClose={closeDialog}
        onSaved={() => {
          closeDialog();
          // Sidebar owns its own connection list and listens for this
          // rather than being handed the new record directly, so App does
          // not need to know how Sidebar's state is shaped.
          window.dispatchEvent(new CustomEvent(CONNECTIONS_CHANGED_EVENT));
        }}
      />
    </div>
  );
}

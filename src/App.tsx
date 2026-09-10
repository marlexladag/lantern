import { useCallback, useEffect, useState } from 'react';
import './styles/tokens.css';
import './App.css';
import { EngineStatus } from './components/EngineStatus';
import { Sidebar, ADD_CONNECTION_EVENT, CONNECTIONS_CHANGED_EVENT } from './components/Sidebar';
import { ConnectionDialog } from './components/ConnectionDialog';

export default function App() {
  const [dialogOpen, setDialogOpen] = useState(false);

  const openDialog = useCallback(() => setDialogOpen(true), []);
  const closeDialog = useCallback(() => setDialogOpen(false), []);

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
        <Sidebar />
        <main className="app-main">
          <span>Select a table to see its data.</span>
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

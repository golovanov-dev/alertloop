import { Navigate, Route, Routes } from "react-router-dom";
import { Layout } from "./components/Layout";
import { Toast } from "./components/Toast";
import { useApp } from "./context";
import { Login, Setup, Unreachable } from "./screens/Login";
import { About } from "./screens/About";
import { Deliveries } from "./screens/Deliveries";
import { EventDetail } from "./screens/EventDetail";
import { Events } from "./screens/Events";
import { Overview } from "./screens/Overview";
import { Users } from "./screens/Users";

export function App() {
  const { auth, toast } = useApp();

  if (auth === "loading") return null;
  if (auth !== "signedIn") {
    return (
      <>
        {auth === "setup" ? <Setup /> : auth === "unreachable" ? <Unreachable /> : <Login />}
        <Toast message={toast} />
      </>
    );
  }

  return (
    <>
      <Layout>
        <Routes>
          <Route path="/" element={<Navigate to="/overview" replace />} />
          <Route path="/overview" element={<Overview />} />
          <Route path="/events" element={<Events />} />
          <Route path="/events/:id" element={<EventDetail />} />
          <Route path="/deliveries" element={<Deliveries />} />
          <Route path="/users" element={<Users />} />
          <Route path="/about" element={<About />} />
          <Route path="*" element={<Navigate to="/overview" replace />} />
        </Routes>
      </Layout>
      <Toast message={toast} />
    </>
  );
}

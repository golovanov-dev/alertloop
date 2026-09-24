import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { api, ApiError, setUnauthorizedHandler, type User } from "./api";

/** Where sign-in stands: asking the server, no users yet, signed out, signed in. */
export type AuthState = "loading" | "setup" | "signedOut" | "signedIn" | "unreachable";

interface AppCtx {
  auth: AuthState;
  user: User | null;
  signedIn: (u: User) => void;
  logout: (everywhere?: boolean) => Promise<void>;
  /** Ask the server again, after it could not be reached. */
  retry: () => void;
  /** Why the last session ended, shown on the sign-in screen. */
  notice: string | null;
  toast: string | null;
  showToast: (msg: string) => void;
}

const Ctx = createContext<AppCtx | null>(null);

export function AppProvider({ children }: { children: ReactNode }) {
  const [auth, setAuth] = useState<AuthState>("loading");
  const [user, setUser] = useState<User | null>(null);
  const [toast, setToast] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const timer = useRef<number | undefined>(undefined);

  const showToast = useCallback((msg: string) => {
    setToast(msg);
    window.clearTimeout(timer.current);
    timer.current = window.setTimeout(() => setToast(null), 2600);
  }, []);

  const check = useCallback(() => {
    setAuth("loading");
    api
      .me()
      .then((u) => {
        setUser(u);
        setAuth("signedIn");
      })
      .catch((e) => {
        setUser(null);
        if (e instanceof ApiError && e.code === "setup_required") setAuth("setup");
        else if (e instanceof ApiError && e.status === 401) setAuth("signedOut");
        else setAuth("unreachable");
      });
  }, []);

  useEffect(check, [check]);

  const signedIn = useCallback((u: User) => {
    setUser(u);
    setAuth("signedIn");
    setNotice(null);
  }, []);

  const logout = useCallback(
    async (everywhere = false) => {
      try {
        await api.logout(everywhere);
      } catch {
        showToast("Could not reach the server; the session may still be open on it.");
      }
      setUser(null);
      setAuth("signedOut");
    },
    [showToast],
  );

  useEffect(() => {
    setUnauthorizedHandler(() => {
      setUser(null);
      setAuth("signedOut");
      setNotice("Your session has ended. Sign in again.");
    });
    return () => setUnauthorizedHandler(null);
  }, []);

  const value = useMemo(
    () => ({ auth, user, signedIn, logout, retry: check, notice, toast, showToast }),
    [auth, user, signedIn, logout, check, notice, toast, showToast],
  );

  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}

export function useApp(): AppCtx {
  const v = useContext(Ctx);
  if (!v) throw new Error("useApp must be used within AppProvider");
  return v;
}

import {
  createContext,
  useCallback,
  useContext,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { clearToken, getToken, setToken } from "./api";

interface AppCtx {
  token: string;
  login: (token: string) => void;
  logout: () => void;
  toast: string | null;
  showToast: (msg: string) => void;
}

const Ctx = createContext<AppCtx | null>(null);

export function AppProvider({ children }: { children: ReactNode }) {
  const [token, setTok] = useState<string>(() => getToken());
  const [toast, setToast] = useState<string | null>(null);
  const timer = useRef<number | undefined>(undefined);

  const showToast = useCallback((msg: string) => {
    setToast(msg);
    window.clearTimeout(timer.current);
    timer.current = window.setTimeout(() => setToast(null), 2600);
  }, []);

  const login = useCallback((t: string) => {
    setToken(t);
    setTok(t);
  }, []);

  const logout = useCallback(() => {
    clearToken();
    setTok("");
  }, []);

  const value = useMemo(
    () => ({ token, login, logout, toast, showToast }),
    [token, login, logout, toast, showToast],
  );

  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}

export function useApp(): AppCtx {
  const v = useContext(Ctx);
  if (!v) throw new Error("useApp must be used within AppProvider");
  return v;
}

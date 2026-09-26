import { resolve } from "node:path";
import { defineConfig } from "vite";
import wails from "@wailsio/runtime/plugins/vite";

// https://vitejs.dev/config/
export default defineConfig({
  server: {
    host: "127.0.0.1",
    port: Number(process.env.WAILS_VITE_PORT) || 9245,
    strictPort: true,
  },
  plugins: [wails("./bindings")],
  build: {
    // Three windows, three entry points: the tray popover (index.html), the
    // startup status window (startup.html), and the update prompt
    // (update.html, see main.go's "Update" window).
    rollupOptions: {
      input: {
        main: resolve(__dirname, "index.html"),
        startup: resolve(__dirname, "startup.html"),
        update: resolve(__dirname, "update.html"),
      },
    },
  },
});

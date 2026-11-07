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
    // One entry point per window: the tray popover (index.html), the startup
    // status window (startup.html), the update prompt (update.html, see
    // main.go's "Update" window), and the windows that remove and add a
    // remote control folder (remove.html and add.html, main.go's
    // "RemoveRoot" and "AddRoot").
    rollupOptions: {
      input: {
        main: resolve(__dirname, "index.html"),
        startup: resolve(__dirname, "startup.html"),
        update: resolve(__dirname, "update.html"),
        remove: resolve(__dirname, "remove.html"),
        add: resolve(__dirname, "add.html"),
      },
    },
  },
});

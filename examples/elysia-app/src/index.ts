import { Elysia } from "elysia";
import { cors } from "@elysiajs/cors";
import { bearer } from "@elysiajs/bearer";
import { HeimdallClient } from "@techsavvyash/heimdall-sdk";
import { readFileSync } from "fs";
import { join } from "path";

// Initialize Heimdall client
const heimdall = new HeimdallClient({
  apiUrl: process.env.HEIMDALL_API_URL || "http://localhost:8080",
  tenantId: process.env.HEIMDALL_TENANT_ID,
  // Server-side storage using in-memory Map
  storage: {
    store: new Map<string, string>(),
    getItem(key: string) {
      return this.store.get(key) || null;
    },
    setItem(key: string, value: string) {
      this.store.set(key, value);
    },
    removeItem(key: string) {
      this.store.delete(key);
    }
  },
  autoRefresh: false, // Server-side doesn't need auto-refresh
});

// Custom storage for user sessions (in production, use Redis)
const userSessions = new Map<string, any>();

const app = new Elysia()
  .use(cors({
    origin: process.env.FRONTEND_URL || "http://localhost:3000",
    credentials: true,
  }))
  .use(bearer())

  // Serve frontend
  .get("/", () => {
    const htmlPath = join(process.cwd(), "public", "index.html");
    const html = readFileSync(htmlPath, "utf-8");
    return new Response(html, {
      headers: { "Content-Type": "text/html" }
    });
  })

  // Public routes
  .get("/api", () => ({
    message: "Heimdall + ElysiaJS Backend API",
    version: "1.0.0",
    status: "healthy"
  }))

  .get("/health", () => ({
    status: "ok",
    timestamp: new Date().toISOString()
  }))

  // Authentication routes
  .post("/api/auth/register", async ({ body, set }) => {
    try {
      const user = await heimdall.auth.register(body as any);
      const accessToken = await heimdall.auth.getAccessToken();

      // Store user session
      if (accessToken) {
        userSessions.set(accessToken, user);
      }

      return {
        success: true,
        data: {
          user,
          accessToken
        }
      };
    } catch (error: any) {
      set.status = error.statusCode || 500;
      return {
        success: false,
        error: {
          code: error.code || "REGISTRATION_FAILED",
          message: error.message || "Registration failed"
        }
      };
    }
  })

  .post("/api/auth/login", async ({ body, set }) => {
    try {
      const user = await heimdall.auth.login(body as any);
      const accessToken = await heimdall.auth.getAccessToken();

      // Store user session
      if (accessToken) {
        userSessions.set(accessToken, user);
      }

      return {
        success: true,
        data: {
          user,
          accessToken
        }
      };
    } catch (error: any) {
      set.status = error.statusCode || 500;
      return {
        success: false,
        error: {
          code: error.code || "LOGIN_FAILED",
          message: error.message || "Login failed"
        }
      };
    }
  })

  .post("/api/auth/logout", async ({ bearer, set }) => {
    try {
      if (!bearer) {
        set.status = 401;
        return {
          success: false,
          error: {
            code: "UNAUTHORIZED",
            message: "No authorization token provided"
          }
        };
      }

      await heimdall.auth.logout();
      userSessions.delete(bearer);

      return {
        success: true,
        message: "Logged out successfully"
      };
    } catch (error: any) {
      set.status = error.statusCode || 500;
      return {
        success: false,
        error: {
          code: error.code || "LOGOUT_FAILED",
          message: error.message || "Logout failed"
        }
      };
    }
  })

  // Protected routes - require authentication
  .get("/api/users/me", async ({ bearer, set }) => {
    try {
      if (!bearer) {
        set.status = 401;
        return {
          success: false,
          error: {
            code: "UNAUTHORIZED",
            message: "Authentication required"
          }
        };
      }

      // Check session
      const user = userSessions.get(bearer);
      if (!user) {
        // Try to get user from Heimdall
        const profile = await heimdall.user.getProfile();
        if (profile) {
          userSessions.set(bearer, profile);
          return {
            success: true,
            data: profile
          };
        }

        set.status = 401;
        return {
          success: false,
          error: {
            code: "UNAUTHORIZED",
            message: "Invalid or expired token"
          }
        };
      }

      return {
        success: true,
        data: user
      };
    } catch (error: any) {
      set.status = error.statusCode || 500;
      return {
        success: false,
        error: {
          code: error.code || "REQUEST_FAILED",
          message: error.message || "Failed to get user profile"
        }
      };
    }
  })

  .patch("/api/users/me", async ({ bearer, body, set }) => {
    try {
      if (!bearer) {
        set.status = 401;
        return {
          success: false,
          error: {
            code: "UNAUTHORIZED",
            message: "Authentication required"
          }
        };
      }

      const updatedUser = await heimdall.user.updateProfile(body as any);

      // Update session
      userSessions.set(bearer, updatedUser);

      return {
        success: true,
        data: updatedUser
      };
    } catch (error: any) {
      set.status = error.statusCode || 500;
      return {
        success: false,
        error: {
          code: error.code || "UPDATE_FAILED",
          message: error.message || "Failed to update profile"
        }
      };
    }
  })

  // Example protected API endpoint
  .get("/api/data", async ({ bearer, set }) => {
    if (!bearer) {
      set.status = 401;
      return {
        success: false,
        error: {
          code: "UNAUTHORIZED",
          message: "Authentication required"
        }
      };
    }

    const user = userSessions.get(bearer);
    if (!user) {
      set.status = 401;
      return {
        success: false,
        error: {
          code: "UNAUTHORIZED",
          message: "Invalid or expired token"
        }
      };
    }

    return {
      success: true,
      data: {
        message: `Hello ${user.firstName || user.email}!`,
        items: [
          { id: 1, name: "Item 1", description: "Protected data 1" },
          { id: 2, name: "Item 2", description: "Protected data 2" },
          { id: 3, name: "Item 3", description: "Protected data 3" }
        ],
        timestamp: new Date().toISOString()
      }
    };
  })

  .listen(process.env.PORT || 3000);

console.log(
  `🦊 ElysiaJS + Heimdall server is running at http://${app.server?.hostname}:${app.server?.port}`
);

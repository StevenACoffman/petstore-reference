/// <reference types="vitest" />
import fs from 'node:fs';
import path from 'node:path';
import { defineConfig, loadEnv } from 'vite';
import react from '@vitejs/plugin-react';

const certPath = path.resolve('../.certs/cert.pem');
const keyPath = path.resolve('../.certs/key.pem');
const hasCerts = fs.existsSync(certPath) && fs.existsSync(keyPath);

// https://vite.dev/config/
export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), '');
  const defaultTarget = hasCerts ? 'https://127.0.0.1:8080' : 'http://127.0.0.1:8080';
  const backendTarget = env.VITE_BACKEND_TARGET || defaultTarget;

  return {
    plugins: [react()],
    server: {
      port: 4321,
      proxy: {
        '/pet.v2.PetService': {
          target: backendTarget,
          changeOrigin: true,
          secure: false,
        },
        '/healthz': {
          target: backendTarget,
          changeOrigin: true,
          secure: false,
        },
      },
      ...(hasCerts
        ? {
            https: {
              cert: fs.readFileSync(certPath),
              key: fs.readFileSync(keyPath),
            },
          }
        : {}),
    },
    preview: {
      port: 4321,
      ...(hasCerts
        ? {
            https: {
              cert: fs.readFileSync(certPath),
              key: fs.readFileSync(keyPath),
            },
          }
        : {}),
    },
    test: {
      environment: 'jsdom',
      globals: true,
      setupFiles: './src/test/setup.ts',
      testTimeout: 15000,
    },
  };
});

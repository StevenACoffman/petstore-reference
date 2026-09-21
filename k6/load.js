// Load profile for the pet service.
//
// The Baggage header is the point of this script: k6 does not send one on its
// own, and without it profiles cannot be sliced by test run or scenario. The
// server turns `k6.*` baggage into pprof labels, so a flame graph in Pyroscope
// can be filtered to `k6_scenario="browse"`.
//
//   just load-test
//   just load-test 2m
import http from 'k6/http';
import { check, fail } from 'k6';

const BASE = __ENV.BASE_URL || 'http://127.0.0.1:8080';
const DURATION = __ENV.DURATION || '30s';

// One id per run, so successive runs stay distinguishable in Pyroscope.
const RUN_ID = __ENV.RUN_ID || `local-${Date.now()}`;

export const options = {
  scenarios: {
    browse: {
      executor: 'constant-vus',
      vus: Number(__ENV.VUS || 5),
      duration: DURATION,
      exec: 'browse',
    },
    write: {
      executor: 'constant-vus',
      vus: 2,
      duration: DURATION,
      exec: 'write',
    },
  },
  thresholds: {
    checks: ['rate>0.99'],
    http_req_failed: ['rate<0.01'],
  },
};

function params(scenario) {
  return {
    headers: {
      'Content-Type': 'application/json',
      Baggage: `k6.test_run_id=${RUN_ID},k6.scenario=${scenario}`,
    },
    tags: { scenario },
  };
}

function rpc(method, body, scenario) {
  return http.post(`${BASE}/pet.v2.PetService/${method}`, JSON.stringify(body), params(scenario));
}

export function browse() {
  const list = rpc('ListPets', { pageSize: 20 }, 'browse');
  check(list, { 'ListPets 200': (r) => r.status === 200 });
  if (list.status !== 200) return;

  const pets = list.json('pets') || [];
  if (pets.length === 0) return;

  const pet = pets[Math.floor(Math.random() * pets.length)];
  const got = rpc('GetPet', { id: pet.id }, 'browse');
  check(got, { 'GetPet 200': (r) => r.status === 200 });
}

export function write() {
  const created = rpc(
    'CreatePet',
    {
      name: `Load ${Math.floor(Math.random() * 1e6)}`,
      species: 'Dog',
      birthDate: '2021-04-04',
      tags: ['load-test'],
    },
    'write',
  );
  if (!check(created, { 'CreatePet 200': (r) => r.status === 200 })) {
    return;
  }

  const id = created.json('pet.id');
  if (!id) fail('CreatePet returned no id');

  // A partial update — the path the field mask exists for.
  const updated = rpc(
    'UpdatePet',
    { id, name: `Renamed ${Math.floor(Math.random() * 1e6)}`, updateMask: 'name' },
    'write',
  );
  check(updated, { 'UpdatePet 200': (r) => r.status === 200 });
}

import React from 'react';
import { useParams, Link, useNavigate } from 'react-router-dom';
import { useQuery, useMutation } from '@connectrpc/connect-query';
import { useQueryClient } from '@tanstack/react-query';
import { PetService } from '../gen/pet/v2/pet_pb';
import { Layout } from '../components/Layout';
import { PetImage } from '../components/PetImage';
import { formatTimestamp, formatBirthDate, calculateAge } from '../lib/date';

export const PetDetails: React.FC = () => {
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();
  const queryClient = useQueryClient();

  const { data, isLoading, error } = useQuery(
    PetService.method.getPet,
    { id: id || '' },
    { enabled: !!id }
  );

  const deleteMutation = useMutation(PetService.method.deletePet, {
    onSuccess: () => {
      queryClient.invalidateQueries();
      navigate('/');
    },
    onError: (err) => {
      alert(`Failed to delete pet: ${err.message || String(err)}`);
    },
  });

  const pet = data?.pet;

  const handleDelete = () => {
    if (pet && confirm(`Are you sure you want to delete ${pet.name}?`)) {
      deleteMutation.mutate({ id: pet.id });
    }
  };

  if (isLoading) {
    return (
      <Layout>
        <div style={{ textAlign: 'center', padding: '3rem', color: 'var(--text-muted)' }}>
          Loading pet details...
        </div>
      </Layout>
    );
  }

  if (error || !pet) {
    return (
      <Layout>
        <div
          style={{
            padding: '2rem',
            background: 'var(--card-bg)',
            border: '1px solid var(--border)',
            borderRadius: '8px',
            textAlign: 'center',
          }}
        >
          <h2 style={{ color: 'var(--danger)', fontSize: '1.15rem', marginBottom: '0.5rem' }}>
            Failed to Load Pet
          </h2>
          <p style={{ color: 'var(--text-muted)', marginBottom: '1.25rem' }}>
            {error?.message || 'Pet not found or server error'}
          </p>
          <Link to="/" className="btn btn-secondary">
            ← Back to Directory
          </Link>
        </div>
      </Layout>
    );
  }

  const statusClass =
    pet.status === 1
      ? 'status-available'
      : pet.status === 2
        ? 'status-pending'
        : pet.status === 3
          ? 'status-adopted'
          : 'status-unspecified';
  const statusName =
    pet.status === 1
      ? 'Available'
      : pet.status === 2
        ? 'Pending'
        : pet.status === 3
          ? 'Adopted'
          : 'Unspecified';

  return (
    <Layout
      breadcrumbs={[
        { label: 'Directory', href: '/' },
        { label: pet.name || 'Pet Details' },
      ]}
    >
      <div
        style={{
          display: 'flex',
          justifyContent: 'space-between',
          alignItems: 'flex-start',
          marginBottom: '1.5rem',
          gap: '1rem',
          flexWrap: 'wrap',
        }}
      >
        <div>
          <div style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', marginBottom: '0.35rem' }}>
            <h1 style={{ fontSize: '1.75rem', fontWeight: 700, color: 'var(--text-contrast)' }}>{pet.name}</h1>
            <span className={`status-badge ${statusClass}`}>{statusName}</span>
          </div>
          <p style={{ color: 'var(--text-muted)', fontSize: '0.95rem' }}>
            {pet.species}
            {pet.birthDate ? ` • ${calculateAge(pet.birthDate)}` : ' • Age unknown'}
          </p>
        </div>

        <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
          <Link to={`/pets/${pet.id}/edit`} className="btn btn-secondary">
            <svg
              width="14"
              height="14"
              viewBox="0 0 24 24"
              fill="none"
              stroke="currentColor"
              strokeWidth="2"
              strokeLinecap="round"
              strokeLinejoin="round"
            >
              <path d="M17 3a2.85 2.83 0 1 1 4 4L7.5 20.5 2 22l1.5-5.5Z" />
              <path d="m15 5 4 4" />
            </svg>
            Edit Pet
          </Link>
          <button
            className="btn btn-danger"
            onClick={handleDelete}
            disabled={deleteMutation.isPending}
          >
            <svg
              width="14"
              height="14"
              viewBox="0 0 24 24"
              fill="none"
              stroke="currentColor"
              strokeWidth="2"
              strokeLinecap="round"
              strokeLinejoin="round"
            >
              <path d="M3 6h18" />
              <path d="M19 6v14c0 1-1 2-2 2H7c-1 0-2-1-2-2V6" />
              <path d="M8 6V4c0-1 1-2 2-2h4c1 0 2 1 2 2v2" />
            </svg>
            Delete
          </button>
        </div>
      </div>

      <div className="details-grid">
        {/* Core Information Card */}
        <div className="card">
          <h2
            style={{
              fontSize: '1.1rem',
              fontWeight: 600,
              color: 'var(--text-contrast)',
              marginBottom: '1.25rem',
              borderBottom: '1px solid var(--border)',
              paddingBottom: '0.75rem',
            }}
          >
            Pet Information
          </h2>

          <div
            style={{
              display: 'grid',
              gridTemplateColumns: '140px 1fr',
              gap: '0.85rem 1rem',
              fontSize: '0.9rem',
            }}
          >
            <div style={{ color: 'var(--text-muted)', fontWeight: 500 }}>Pet ID</div>
            <div>
              <code
                style={{
                  color: 'var(--text-primary)',
                  background: 'var(--badge-bg)',
                  padding: '0.15rem 0.4rem',
                  borderRadius: '4px',
                  fontSize: '0.8rem',
                }}
              >
                {pet.id}
              </code>
            </div>

            <div style={{ color: 'var(--text-muted)', fontWeight: 500 }}>Species</div>
            <div style={{ color: 'var(--text-contrast)' }}>{pet.species}</div>

            <div style={{ color: 'var(--text-muted)', fontWeight: 500 }}>Birth Date</div>
            <div style={{ color: 'var(--text-contrast)' }}>
              {formatBirthDate(pet.birthDate, pet.birthDateEstimated)}
              {pet.birthDate && (
                <span style={{ color: 'var(--text-muted)', fontSize: '0.85rem', marginLeft: '0.5rem' }}>
                  ({calculateAge(pet.birthDate)})
                </span>
              )}
            </div>

            <div style={{ color: 'var(--text-muted)', fontWeight: 500 }}>Status</div>
            <div style={{ color: 'var(--text-contrast)' }}>{statusName}</div>

            <div style={{ color: 'var(--text-muted)', fontWeight: 500 }}>Tags</div>
            <div style={{ display: 'flex', flexWrap: 'wrap', gap: '0.35rem' }}>
              {pet.tags && pet.tags.length > 0 ? (
                pet.tags.map((tag, i) => (
                  <span
                    key={i}
                    style={{
                      background: 'var(--badge-bg)',
                      border: '1px solid var(--border)',
                      color: 'var(--text-primary)',
                      padding: '0.15rem 0.5rem',
                      borderRadius: '4px',
                      fontSize: '0.8rem',
                    }}
                  >
                    {tag}
                  </span>
                ))
              ) : (
                <span style={{ color: 'var(--text-muted)', fontSize: '0.85rem' }}>No tags</span>
              )}
            </div>

            <div style={{ color: 'var(--text-muted)', fontWeight: 500 }}>Photos</div>
            <div>
              {pet.photoUrls && pet.photoUrls.length > 0 ? (
                <div style={{ display: 'flex', flexWrap: 'wrap', gap: '0.75rem' }}>
                  {pet.photoUrls.map((url, i) => (
                    <a
                      key={i}
                      href={url}
                      target="_blank"
                      rel="noopener noreferrer"
                      style={{
                        display: 'block',
                        border: '1px solid var(--border)',
                        borderRadius: '6px',
                        overflow: 'hidden',
                        backgroundColor: 'var(--bg-secondary)',
                      }}
                      title="View full image"
                    >
                      <PetImage
                        src={url}
                        alt={`${pet.name} photo ${i + 1}`}
                        size={120}
                      />
                    </a>
                  ))}
                </div>
              ) : (
                <div style={{ color: 'var(--text-muted)', fontSize: '0.85rem' }}>
                  No photos available.
                </div>
              )}
            </div>
          </div>
        </div>

        {/* Audit & System Metadata Card */}
        <div className="card">
          <h2
            style={{
              fontSize: '1.1rem',
              fontWeight: 600,
              color: 'var(--text-contrast)',
              marginBottom: '1.25rem',
              borderBottom: '1px solid var(--border)',
              paddingBottom: '0.75rem',
            }}
          >
            Audit & Identity
          </h2>

          <div style={{ display: 'flex', flexDirection: 'column', gap: '1rem', fontSize: '0.85rem' }}>
            <div>
              <div
                style={{
                  color: 'var(--text-muted)',
                  fontSize: '0.75rem',
                  textTransform: 'uppercase',
                  letterSpacing: '0.03em',
                  marginBottom: '0.25rem',
                }}
              >
                Created By
              </div>
              <div style={{ color: 'var(--text-primary)', fontFamily: 'monospace' }}>
                {pet.createdBy || 'unknown'}
              </div>
            </div>

            <div>
              <div
                style={{
                  color: 'var(--text-muted)',
                  fontSize: '0.75rem',
                  textTransform: 'uppercase',
                  letterSpacing: '0.03em',
                  marginBottom: '0.25rem',
                }}
              >
                Created At
              </div>
              <div style={{ color: 'var(--text-primary)' }}>{formatTimestamp(pet.createdAt)}</div>
            </div>

            <div>
              <div
                style={{
                  color: 'var(--text-muted)',
                  fontSize: '0.75rem',
                  textTransform: 'uppercase',
                  letterSpacing: '0.03em',
                  marginBottom: '0.25rem',
                }}
              >
                Modified By
              </div>
              <div style={{ color: 'var(--text-primary)', fontFamily: 'monospace' }}>
                {pet.modifiedBy || 'unknown'}
              </div>
            </div>

            <div>
              <div
                style={{
                  color: 'var(--text-muted)',
                  fontSize: '0.75rem',
                  textTransform: 'uppercase',
                  letterSpacing: '0.03em',
                  marginBottom: '0.25rem',
                }}
              >
                Modified At
              </div>
              <div style={{ color: 'var(--text-primary)' }}>{formatTimestamp(pet.modifiedAt)}</div>
            </div>
          </div>
        </div>
      </div>

      <div style={{ marginTop: '2rem' }}>
        <Link to="/" className="btn btn-secondary">
          ← Back to Pets Directory
        </Link>
      </div>
    </Layout>
  );
};

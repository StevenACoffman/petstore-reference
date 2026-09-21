import React, { useEffect, useState, useMemo } from 'react';
import { Link } from 'react-router-dom';
import { useQuery, useMutation } from '@connectrpc/connect-query';
import { useQueryClient } from '@tanstack/react-query';
import { PetService, PetStatus } from '../gen/pet/v2/pet_pb';
import { Layout } from '../components/Layout';
import { PetImage } from '../components/PetImage';
import { formatTimestamp, calculateAge } from '../lib/date';

export const PetList: React.FC = () => {
  const queryClient = useQueryClient();
  const [currentFilter, setCurrentFilter] = useState<'all' | '1' | '2' | '3'>('all');
  const [speciesFilter, setSpeciesFilter] = useState('');
  const [searchQuery, setSearchQuery] = useState('');
  // Cursor paging is forward-only, so "previous" is a pop off the stack of tokens
  // we have already followed. An empty stack is the first page.
  const [pageTokens, setPageTokens] = useState<string[]>([]);
  const pageSize = 20;
  const currentPage = pageTokens.length;

  const { data, isLoading, error, refetch } = useQuery(PetService.method.listPets, {
    pageSize,
    pageToken: pageTokens[pageTokens.length - 1] ?? '',
    status:
      currentFilter === 'all'
        ? PetStatus.UNSPECIFIED
        : (Number(currentFilter) as PetStatus),
    species: speciesFilter.trim() || undefined,
  });

  useEffect(() => {
    setPageTokens([]);
  }, [currentFilter, speciesFilter]);

  const deleteMutation = useMutation(PetService.method.deletePet, {
    onSuccess: () => {
      queryClient.invalidateQueries();
    },
    onError: (err) => {
      alert(`Delete failed: ${err.message || String(err)}`);
    },
  });

  const pets = data?.pets || [];
  const totalCount = data?.totalCount || 0;
  const totalPages = Math.max(1, Math.ceil(totalCount / pageSize));
  const nextPageToken = data?.nextPageToken || '';

  const filteredPets = useMemo(() => {
    let list = pets;
    if (searchQuery) {
      const q = searchQuery.toLowerCase();
      list = list.filter(
        (p) =>
          p.name.toLowerCase().includes(q) ||
          p.species.toLowerCase().includes(q) ||
          p.tags.some((t) => t.toLowerCase().includes(q))
      );
    }
    return list;
  }, [pets, searchQuery]);

  const handleDelete = (id: string, name: string) => {
    if (confirm(`Are you sure you want to delete ${name}?`)) {
      deleteMutation.mutate({ id });
    }
  };

  return (
    <Layout>
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
          <div style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', marginBottom: '0.25rem' }}>
            <h1 style={{ fontSize: '1.75rem', fontWeight: 700, color: 'var(--text-contrast)' }}>Pets Directory</h1>
            <span className="badge">
              {totalCount} {totalCount === 1 ? 'pet' : 'pets'}
            </span>
          </div>
          <p style={{ color: 'var(--text-muted)', fontSize: '0.9rem' }}>
            Manage and inspect pet records across the microservice.
          </p>
        </div>

        <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
          <button className="btn btn-secondary" onClick={() => refetch()}>
            ↻ Refresh
          </button>
          <Link to="/pets/new" className="btn btn-primary">
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
              <path d="M5 12h14" />
              <path d="M12 5v14" />
            </svg>
            Add Pet
          </Link>
        </div>
      </div>

      {/* Filter & Search Toolbar */}
      <div className="card" style={{ marginBottom: '1.5rem', padding: '1rem 1.25rem' }}>
        <div
          style={{
            display: 'flex',
            justifyContent: 'space-between',
            alignItems: 'center',
            gap: '1rem',
            flexWrap: 'wrap',
          }}
        >
          <div className="filter-bar">
            <button
              className={`filter-tab ${currentFilter === 'all' ? 'active' : ''}`}
              onClick={() => setCurrentFilter('all')}
            >
              All
            </button>
            <button
              className={`filter-tab ${currentFilter === '1' ? 'active' : ''}`}
              onClick={() => setCurrentFilter('1')}
            >
              Available
            </button>
            <button
              className={`filter-tab ${currentFilter === '2' ? 'active' : ''}`}
              onClick={() => setCurrentFilter('2')}
            >
              Pending
            </button>
            <button
              className={`filter-tab ${currentFilter === '3' ? 'active' : ''}`}
              onClick={() => setCurrentFilter('3')}
            >
              Adopted
            </button>
          </div>

          <div style={{ display: 'flex', gap: '0.5rem', flex: 1, maxWidth: '460px', flexWrap: 'wrap' }}>
            <input
              type="text"
              placeholder="Filter by species (e.g. Dog)..."
              value={speciesFilter}
              onChange={(e) => setSpeciesFilter(e.target.value)}
              style={{ padding: '0.45rem 0.75rem', fontSize: '0.85rem', flex: '1 1 140px' }}
            />
            <input
              type="text"
              placeholder="Search this page by name, species, or tags..."
              value={searchQuery}
              onChange={(e) => setSearchQuery(e.target.value)}
              style={{ padding: '0.45rem 0.75rem', fontSize: '0.85rem', flex: '2 1 180px' }}
            />
          </div>
        </div>
      </div>

      {/* Directory Content */}
      {isLoading ? (
        <div style={{ textAlign: 'center', padding: '3rem', color: 'var(--text-muted)' }}>
          Loading pets...
        </div>
      ) : error ? (
        <div style={{ color: '#ef4444', textAlign: 'center', padding: '2rem' }}>
          Failed to load pets: {error.message || String(error)}
        </div>
      ) : filteredPets.length === 0 ? (
        <div className="empty-state">
          <h3>No pets found</h3>
          <p>
            {pets.length === 0
              ? 'Your directory is currently empty. Add your first pet to get started!'
              : 'No pets match your current filter or search criteria.'}
          </p>
          {pets.length === 0 && (
            <div style={{ marginTop: '1rem' }}>
              <Link to="/pets/new" className="btn btn-primary">
                Add New Pet
              </Link>
            </div>
          )}
        </div>
      ) : (
        <>
          <div className="pets-list">
          {filteredPets.map((pet) => {
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
            const createdBy = pet.createdBy || 'unknown';
            const modifiedBy = pet.modifiedBy || 'unknown';
            const createdDate = formatTimestamp(pet.createdAt);
            const modifiedDate = formatTimestamp(pet.modifiedAt);

            return (
              <div key={pet.id} className="pet-item">
                <div className="pet-header">
                  <div className="pet-title-group" style={{ display: 'flex', alignItems: 'center', gap: '0.6rem' }}>
                    <PetImage
                      src={pet.photoUrls?.[0]}
                      alt={pet.name}
                      size={32}
                    />
                    <Link to={`/pets/${pet.id}`} className="pet-name">
                      {pet.name}
                    </Link>
                    <span className={`status-badge ${statusClass}`}>{statusName}</span>
                  </div>
                  <div className="pet-actions">
                    <Link to={`/pets/${pet.id}`} className="action-btn action-edit" title="View Details">
                      View
                    </Link>
                    <Link
                      to={`/pets/${pet.id}/edit`}
                      className="action-btn action-edit"
                      title="Edit Pet"
                    >
                      <svg
                        width="13"
                        height="13"
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
                      Edit
                    </Link>
                    <button
                      className="action-btn action-delete"
                      onClick={() => handleDelete(pet.id, pet.name)}
                      title="Delete Pet"
                      disabled={deleteMutation.isPending}
                    >
                      <svg
                        width="13"
                        height="13"
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

                <div className="pet-details-row">
                  <span>{pet.species}</span>
                  <span className="meta-dot">·</span>
                  <span>
                    {pet.birthDate
                      ? `${calculateAge(pet.birthDate)}${pet.birthDateEstimated ? ' (est.)' : ''}`
                      : 'Age unknown'}
                  </span>
                  {pet.tags && pet.tags.length > 0 && (
                    <>
                      <span className="meta-dot">·</span>
                      <span style={{ display: 'inline-flex', gap: '0.3rem', flexWrap: 'wrap' }}>
                        {pet.tags.map((tag, i) => (
                          <span
                            key={i}
                            style={{
                              background: 'var(--badge-bg)',
                              border: '1px solid var(--border)',
                              color: 'var(--text-secondary)',
                              padding: '0.05rem 0.4rem',
                              borderRadius: '4px',
                              fontSize: '0.75rem',
                            }}
                          >
                            {tag}
                          </span>
                        ))}
                      </span>
                    </>
                  )}
                </div>

                <div className="pet-footer">
                  <div className="audit-line">
                    <span className="label">Created:</span>
                    <span className="user">{createdBy}</span>
                    {createdDate && <span className="time">on {createdDate}</span>}
                  </div>
                  {modifiedBy &&
                    modifiedBy !== 'unknown' &&
                    (modifiedBy !== createdBy || modifiedDate !== createdDate) && (
                      <div className="audit-line">
                        <span className="label">Updated:</span>
                        <span className="user">{modifiedBy}</span>
                        {modifiedDate && <span className="time">on {modifiedDate}</span>}
                      </div>
                    )}
                </div>
              </div>
            );
          })}
          </div>
          {totalPages > 1 && (
            <nav
              aria-label="Pet directory pagination"
              style={{
                display: 'flex',
                justifyContent: 'center',
                alignItems: 'center',
                gap: '0.75rem',
                marginTop: '1.5rem',
              }}
            >
              <button
                className="btn btn-secondary"
                disabled={currentPage === 0}
                onClick={() => setPageTokens((tokens) => tokens.slice(0, -1))}
              >
                Previous
              </button>
              <span style={{ color: 'var(--text-muted)', fontSize: '0.875rem' }}>
                Page {currentPage + 1} of {totalPages}
              </span>
              <button
                className="btn btn-secondary"
                disabled={!nextPageToken}
                onClick={() => setPageTokens((tokens) => [...tokens, nextPageToken])}
              >
                Next
              </button>
            </nav>
          )}
        </>
      )}
    </Layout>
  );
};

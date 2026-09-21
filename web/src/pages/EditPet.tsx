import React, { useState, useEffect } from 'react';
import { useParams, Link, useNavigate } from 'react-router-dom';
import { useQuery, useMutation } from '@connectrpc/connect-query';
import { useQueryClient } from '@tanstack/react-query';
import { PetService, PetStatus } from '../gen/pet/v2/pet_pb';
import { Layout } from '../components/Layout';
import { PetFormFields } from '../components/PetFormFields';
import { errorMessage } from '../lib/errors';

function formatDate(ts?: { seconds: bigint }): string {
  if (!ts || !ts.seconds) return 'N/A';
  const d = new Date(Number(ts.seconds) * 1000);
  return d.toLocaleString(undefined, {
    month: 'short',
    day: 'numeric',
    year: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
  });
}

export const EditPet: React.FC = () => {
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();
  const queryClient = useQueryClient();

  const { data, isLoading, error } = useQuery(
    PetService.method.getPet,
    { id: id || '' },
    { enabled: !!id }
  );

  const [name, setName] = useState('');
  const [species, setSpecies] = useState('');
  const [birthDate, setBirthDate] = useState('');
  const [birthDateEstimated, setBirthDateEstimated] = useState(false);
  const [status, setStatus] = useState<PetStatus>(PetStatus.AVAILABLE);
  const [tags, setTags] = useState('');
  const [photoUrls, setPhotoUrls] = useState<string[]>([]);
  const [validationError, setValidationError] = useState<string | null>(null);

  const pet = data?.pet;

  useEffect(() => {
    if (pet) {
      setName(pet.name);
      setSpecies(pet.species);
      setBirthDate(pet.birthDate);
      setBirthDateEstimated(pet.birthDateEstimated);
      setStatus(pet.status || PetStatus.AVAILABLE);
      setTags((pet.tags || []).join(', '));
      setPhotoUrls(pet.photoUrls || []);
    }
  }, [pet]);

  const updateMutation = useMutation(PetService.method.updatePet, {
    onSuccess: () => {
      queryClient.invalidateQueries();
      navigate(`/pets/${id}`);
    },
    onError: (err) => {
      setValidationError(errorMessage(err));
    },
  });

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    setValidationError(null);

    const tagList = tags
      .split(',')
      .map((t) => t.trim())
      .filter(Boolean);
    updateMutation.mutate({
      id: id || '',
      name: name.trim(),
      species: species.trim(),
      birthDate,
      birthDateEstimated,
      status,
      tags: tagList,
      photoUrls,
    });
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
            Pet Not Found
          </h2>
          <p style={{ color: 'var(--text-muted)', fontSize: '0.875rem', marginBottom: '1.25rem' }}>
            {error?.message || 'The requested pet could not be loaded.'}
          </p>
          <Link to="/" className="btn btn-secondary">
            ← Back to Directory
          </Link>
        </div>
      </Layout>
    );
  }

  return (
    <Layout
      breadcrumbs={[
        { label: 'Pets', href: '/' },
        { label: pet.name, href: `/pets/${pet.id}` },
        { label: 'Edit' },
      ]}
    >
      <div className="card" style={{ maxWidth: '680px', margin: '0 auto' }}>
        <div className="card-header">
          <h1 className="card-title">Edit Pet: {pet.name}</h1>
          <p className="card-subtitle">
            Update pet attributes. Modification audit logs will be updated automatically.
          </p>
        </div>

        {validationError && (
          <div className="validation-alert">
            <strong>⚠️ Validation Error</strong>
            <div>{validationError}</div>
          </div>
        )}

        <form onSubmit={handleSubmit}>
          <PetFormFields
            name={name} species={species} birthDate={birthDate} birthDateEstimated={birthDateEstimated}
            status={status} tags={tags} photoUrls={photoUrls}
            onNameChange={setName} onSpeciesChange={setSpecies}
            onBirthDateChange={setBirthDate} onBirthDateEstimatedChange={setBirthDateEstimated}
            onStatusChange={setStatus} onTagsChange={setTags} onPhotoUrlsChange={setPhotoUrls}
          />

          <div
            style={{
              background: 'var(--badge-bg)',
              border: '1px solid var(--border)',
              borderRadius: '6px',
              padding: '0.85rem 1rem',
              marginBottom: '1.25rem',
              fontSize: '0.8rem',
              color: 'var(--text-disabled)',
            }}
          >
            <div style={{ fontWeight: 500, color: 'var(--text-secondary)', marginBottom: '0.35rem' }}>
              Audit Information
            </div>
            <div>
              Created by{' '}
              <span style={{ color: 'var(--text-primary)', fontFamily: 'monospace' }}>
                {pet.createdBy || 'unknown'}
              </span>{' '}
              on {formatDate(pet.createdAt)}
              {pet.modifiedBy && pet.modifiedBy !== 'unknown' && (
                <>
                  <br />
                  Last modified by{' '}
                  <span style={{ color: 'var(--text-primary)', fontFamily: 'monospace' }}>
                    {pet.modifiedBy}
                  </span>{' '}
                  on {formatDate(pet.modifiedAt)}
                </>
              )}
            </div>
          </div>

          <div className="form-actions">
            <button
              type="submit"
              className="btn btn-primary"
              disabled={updateMutation.isPending}
            >
              {updateMutation.isPending ? 'Saving...' : 'Save Changes'}
            </button>
            <Link to={`/pets/${pet.id}`} className="btn btn-secondary">
              Cancel
            </Link>
          </div>
        </form>
      </div>
    </Layout>
  );
};

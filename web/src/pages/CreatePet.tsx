import React, { useState } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import { useMutation } from '@connectrpc/connect-query';
import { useQueryClient } from '@tanstack/react-query';
import { PetService, PetStatus } from '../gen/pet/v2/pet_pb';
import { Layout } from '../components/Layout';
import { PetFormFields } from '../components/PetFormFields';
import { errorMessage } from '../lib/errors';

export const CreatePet: React.FC = () => {
  const navigate = useNavigate();
  const queryClient = useQueryClient();

  const [name, setName] = useState('');
  const [species, setSpecies] = useState('');
  const [birthDate, setBirthDate] = useState('');
  const [birthDateEstimated, setBirthDateEstimated] = useState(false);
  const [status, setStatus] = useState<PetStatus>(PetStatus.AVAILABLE);
  const [tags, setTags] = useState('');
  const [photoUrls, setPhotoUrls] = useState<string[]>([]);
  const [validationError, setValidationError] = useState<string | null>(null);
  const [isSubmitting, setIsSubmitting] = useState(false);

  const createMutation = useMutation(PetService.method.createPet);

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setValidationError(null);
    setIsSubmitting(true);

    const tagList = tags
      .split(',')
      .map((t) => t.trim())
      .filter(Boolean);
    try {
      const res = await createMutation.mutateAsync({
        name: name.trim(),
        species: species.trim(),
        birthDate,
        birthDateEstimated,
        status,
        tags: tagList,
        photoUrls,
      });

      queryClient.invalidateQueries();
      if (res.pet?.id) {
        navigate(`/pets/${res.pet.id}`);
      } else {
        navigate('/');
      }
    } catch (err: unknown) {
      setValidationError(errorMessage(err));
    } finally {
      setIsSubmitting(false);
    }
  };

  return (
    <Layout
      breadcrumbs={[
        { label: 'Pets', href: '/' },
        { label: 'New Pet' },
      ]}
    >
      <div className="card" style={{ maxWidth: '680px', margin: '0 auto' }}>
        <div className="card-header">
          <h1 className="card-title">Add New Pet</h1>
          <p className="card-subtitle">
            Register a new pet in the microservice directory.
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

          <div className="form-actions">
            <button
              type="submit"
              className="btn btn-primary"
              disabled={isSubmitting}
            >
              {isSubmitting ? 'Creating...' : 'Create Pet'}
            </button>
            <Link to="/" className="btn btn-secondary">
              Cancel
            </Link>
          </div>
        </form>
      </div>
    </Layout>
  );
};
